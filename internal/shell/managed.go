// 本文件实现受管 SSH 会话：命令经独立控制通道提交，终端字节经 PTY 转发。
package shell

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"whos-typing/internal/control"
	"whos-typing/internal/sshlink"
)

// managedRequest 是一次 CLI 执行；id 用于把完成通知关联到本次请求。
type managedRequest struct {
	id        string
	command   string
	output    *os.File
	done      chan struct{}
	result    control.Result
	sent      bool
	started   bool
	abandoned bool
	err       error
}

type managedOut struct {
	data    []byte
	req     *managedRequest
	command bool
	paste   *managedPaste
	close   bool
}

// ManagedSession 持有经复用 SSH 连接启动的远端 Bash 及其执行通道。
type ManagedSession struct {
	dir    string
	target string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *boundedBuffer

	termRead  *os.File
	termWrite *os.File

	mu                  sync.Mutex
	cond                *sync.Cond
	out                 []managedOut
	closed              bool
	disconnected        bool
	unknownOnDisconnect bool
	endErr              error
	bashExit            *int
	sshExit             int

	owner        ControlOwner
	onControl    func(ControlOwner)
	onState      func(bool)
	titleContext TitleContext
	onTitle      func()

	bashReady      bool // Agent 模式下 Bash 空闲，可接收命令
	humanPrompt    bool // 人工模式下停在空提示符
	humanRunning   bool
	commandRunning bool // 远端命令运行中，含 CLI 已放弃等待的请求
	bootstrapped   bool

	active      *managedRequest
	unconfirmed map[*managedRequest]struct{}
	nextID      uint64

	keys      uint64
	submitted uint64
	pending   int
	dirty     bool

	inputBlocked bool

	paste        *managedPaste
	pasteVersion uint64
	pasteSeq     uint64

	idleStarted time.Time
	idleVersion uint64
	idleCancel  func()
	idleNow     func() time.Time
	idleAfter   func(time.Duration, func()) func()
	beforeWrite func() // 测试钩子：在每次写入前调用

	started    chan struct{}
	startupErr error
	readerDone chan struct{}
	closeOnce  sync.Once
}

// boundedBuffer 保留 stderr 尾部用于启动失败诊断。
type boundedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 8192 {
		b.data = b.data[len(b.data)-8192:]
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// NewManaged 通过已验证的复用连接启动受管 Bash；失败时回收本次资源，不回退直连。
func NewManaged(dir string, link *sshlink.Link) (*ManagedSession, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("生成会话标识失败: %w", err)
	}
	syncToken := hex.EncodeToString(random[:])
	cmd, err := link.OpenChannel(managedRemoteCommand(syncToken))
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("创建受管会话输入通道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("创建受管会话输出通道失败: %w", err)
	}
	stderr := &boundedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("启动受管会话失败: %w", err)
	}
	termRead, termWrite, err := os.Pipe()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("创建终端缓冲失败: %w", err)
	}
	s := &ManagedSession{
		dir: dir, target: link.Target, cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr,
		termRead: termRead, termWrite: termWrite,
		titleContext: InitialTitleContext(), started: make(chan struct{}), readerDone: make(chan struct{}),
		unconfirmed: make(map[*managedRequest]struct{}),
	}
	s.cond = sync.NewCond(&s.mu)
	go s.writer()
	go s.reader(syncToken)
	select {
	case <-s.started:
		return s, nil
	case <-s.readerDone:
		s.mu.Lock()
		err := s.startupErr
		s.mu.Unlock()
		_ = s.Close()
		if err == nil {
			err = errors.New("远端执行组件提前退出")
		}
		return nil, fmt.Errorf("启动受管会话失败: %w: %s", err, strings.TrimSpace(stderr.String()))
	case <-time.After(20 * time.Second):
		_ = s.Close()
		return nil, fmt.Errorf("启动受管会话超时: %s", strings.TrimSpace(stderr.String()))
	}
}

// Target 返回创建会话时使用的 SSH 目标。
func (s *ManagedSession) Target() string { return s.target }

func (s *ManagedSession) enqueueLocked(m managedOut) {
	s.out = append(s.out, m)
	s.cond.Broadcast()
}

func (s *ManagedSession) sendMessageLocked(v map[string]any, req *managedRequest) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.enqueueLocked(managedOut{data: append(data, '\n'), req: req, command: v["t"] == "exec"})
}

func (s *ManagedSession) writer() {
	for {
		s.mu.Lock()
		for len(s.out) == 0 && !s.closed {
			s.cond.Wait()
		}
		if len(s.out) == 0 {
			s.mu.Unlock()
			return
		}
		hook := s.beforeWrite
		s.mu.Unlock()
		if hook != nil {
			hook()
		}
		s.mu.Lock()
		if len(s.out) == 0 {
			s.mu.Unlock()
			continue
		}
		m := s.out[0]
		s.out = s.out[1:]
		if m.command {
			m.req.sent = true
		}
		if m.paste != nil && !s.preparePasteWriteLocked(&m) {
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()
		if _, err := s.stdin.Write(m.data); err != nil {
			s.failChannel(fmt.Errorf("发送受管会话消息失败: %w", err))
			return
		}
		if m.close {
			_ = s.stdin.Close()
			return
		}
	}
}

// failChannel 在写入失败时结束通道；结果由读侧统一按未知处理。
func (s *ManagedSession) failChannel(err error) {
	s.mu.Lock()
	if s.endErr == nil {
		s.endErr = err
	}
	s.mu.Unlock()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func (s *ManagedSession) reader(syncToken string) {
	defer close(s.readerDone)
	r := bufio.NewReader(s.stdout)
	marker := []byte("WT-SYNC:" + syncToken + "\n")
	var prefix []byte
	for {
		line, err := r.ReadBytes('\n')
		prefix = append(prefix, line...)
		if bytes.Contains(prefix, marker) {
			break
		}
		if len(prefix) > 1<<20 {
			prefix = prefix[len(prefix)-len(marker):]
		}
		if err != nil {
			s.ended(fmt.Errorf("远端执行组件未就绪: %w", err))
			return
		}
	}
	header := make([]byte, 5)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			s.ended(nil)
			return
		}
		size := int(header[1])<<24 | int(header[2])<<16 | int(header[3])<<8 | int(header[4])
		if size < 0 || size > 16<<20 {
			s.ended(errors.New("远端执行组件帧格式错误"))
			return
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(r, payload); err != nil {
			s.ended(nil)
			return
		}
		switch header[0] {
		case 'O':
			s.output(payload)
		case 'S':
			s.status(string(payload))
		case 'X':
			var info struct {
				Exit  *int   `json:"exit"`
				Error string `json:"error"`
			}
			if json.Unmarshal(payload, &info) == nil {
				s.mu.Lock()
				s.bashExit = info.Exit
				if info.Error != "" && s.startupErr == nil {
					s.startupErr = errors.New(info.Error)
				}
				s.mu.Unlock()
			}
		}
	}
}

// output 把 PTY 字节送入终端缓冲，并捕获当前请求的输出。
func (s *ManagedSession) output(p []byte) {
	s.mu.Lock()
	s.pasteVersion++
	if r := s.active; r != nil && !r.abandoned && r.err == nil {
		if _, err := r.output.Write(p); err != nil {
			r.err = fmt.Errorf("保存命令输出失败: %w", err)
		}
	}
	s.mu.Unlock()
	_, _ = s.termWrite.Write(p)
}

// display 在终端流中插入普通文本；命令文本先转义控制字符。
func (s *ManagedSession) display(text string) {
	_, _ = s.termWrite.Write([]byte(text))
}

func sanitizeCommandText(command string) string {
	var out strings.Builder
	for _, r := range command {
		switch {
		case r == '\n':
			out.WriteString("\r\n")
		case r == '\t':
			out.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			out.WriteString(strconv.QuoteRune(r)[1 : len(strconv.QuoteRune(r))-1])
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func decodeManagedField(value string, limit int) (string, bool) {
	data, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(data) == 0 || len(data) > limit || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return "", false
	}
	return string(data), true
}

// status 处理独立状态通道；终端输出不能充当协议消息。
func (s *ManagedSession) status(line string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	var displays []string
	s.mu.Lock()
	switch fields[0] {
	case "META":
		if len(fields) == 4 {
			user, ok1 := decodeManagedField(fields[1], 256)
			host, ok2 := decodeManagedField(fields[2], 256)
			dir, ok3 := decodeManagedField(fields[3], 4096)
			if ok1 && ok2 && ok3 {
				s.setTitleContext(TitleContext{User: user, Host: host, Directory: dir})
			}
		}
	case "READY":
		if !s.bootstrapped {
			s.bootstrapped = true
			s.sendMessageLocked(map[string]any{"t": "human"}, nil)
		}
		s.humanPrompt = false
		s.bashReady = s.active == nil
		if len(fields) == 2 {
			s.updateDirLocked(fields[1])
		}
		if s.owner == ControlAgent && s.bashReady {
			s.startIdle()
		}
	case "RUN":
		if len(fields) == 2 && s.active != nil && s.active.id == fields[1] {
			s.active.started = true
			s.commandRunning = true
		}
	case "DONE":
		if len(fields) != 4 {
			break
		}
		code, err := strconv.Atoi(fields[2])
		if err != nil || code < 0 || code > 255 {
			break
		}
		s.updateDirLocked(fields[3])
		s.commandRunning = false
		if r := s.active; r != nil && r.id == fields[1] {
			s.active = nil
			s.state(false)
			s.bashReady = true
			displays = append(displays, fmt.Sprintf("\r\n[CLI 完成，退出码 %d]\r\n", code))
			s.finish(r, code, r.err)
			if s.owner == ControlHumanTakeover {
				s.bashReady = false
				s.sendMessageLocked(map[string]any{"t": "human"}, nil)
			} else if s.owner == ControlAgent {
				s.startIdle()
			}
		} else {
			for r := range s.unconfirmed {
				if r.id == fields[1] {
					delete(s.unconfirmed, r)
					displays = append(displays, fmt.Sprintf("\r\n[CLI 完成，退出码 %d（等待已超时）]\r\n", code))
					// 迟到的完成通知同样恢复会话就绪；人工接管时交回输入归属。
					if s.active == nil {
						if s.owner == ControlHumanTakeover {
							s.sendMessageLocked(map[string]any{"t": "human"}, nil)
						} else if s.owner == ControlAgent {
							s.bashReady = true
							s.startIdle()
						}
					}
					break
				}
			}
		}
	case "POK", "PFAIL":
		if len(fields) == 2 && s.paste != nil && s.paste.id == fields[1] {
			r := s.paste
			if fields[0] == "POK" {
				s.trackInputLocked(r.payload)
				s.finishPasteLocked(r, nil)
			} else {
				s.finishPasteLocked(r, control.ErrPasteUnknown)
			}
		}
	case "HPROMPT":
		s.humanPrompt = true
		s.humanRunning = false
		select {
		case <-s.started:
		default:
			close(s.started)
		}
	case "HRUN":
		s.humanPrompt = false
		s.humanRunning = true
	case "HDONE":
		s.humanRunning = false
		if s.pending > 0 {
			s.pending--
		}
		if len(fields) == 3 {
			s.updateDirLocked(fields[2])
		}
	}
	s.mu.Unlock()
	for _, text := range displays {
		s.display(text)
	}
}

func (s *ManagedSession) updateDirLocked(encoded string) {
	if dir, ok := decodeManagedField(encoded, 4096); ok {
		value := s.titleContext
		value.Directory = dir
		value.Unknown = false
		s.setTitleContext(value)
	}
}

// ended 在 SSH 通道结束时统一处理：没有完成通知的请求一律报告结果未知。
func (s *ManagedSession) ended(channelErr error) {
	waitErr := s.cmd.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disconnected {
		return
	}
	s.disconnected = true
	s.sshExit = 0
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		s.sshExit = exit.ExitCode()
	}
	if channelErr != nil && s.endErr == nil {
		s.endErr = channelErr
	}
	reason := "SSH 连接已断开，结果未知；请显式新建受管会话"
	if s.bashExit != nil {
		reason = fmt.Sprintf("远端 Bash 已退出（退出码 %d），结果未知；请显式新建受管会话", *s.bashExit)
	}
	if s.endErr != nil {
		reason = s.endErr.Error() + "；结果未知"
	}
	if !s.bootstrapped && s.startupErr == nil {
		s.startupErr = errors.New(reason)
	}
	if r := s.active; r != nil {
		s.active = nil
		s.state(false)
		if r.sent {
			s.unknownOnDisconnect = true
			s.finish(r, 1, errors.New(reason))
		} else {
			s.dropQueuedLocked(r)
			s.finish(r, 1, errors.New("受管会话已结束，命令未发送"))
		}
	}
	s.unknownOnDisconnect = s.unknownOnDisconnect || len(s.unconfirmed) != 0
	for r := range s.unconfirmed {
		delete(s.unconfirmed, r)
	}
	s.bashReady = false
	s.humanPrompt = false
	s.commandRunning = false
	s.stopIdle()
	if r := s.paste; r != nil {
		s.dropQueuedPasteLocked(r)
		if r.sent {
			s.finishPasteLocked(r, control.ErrPasteUnknown)
		} else {
			s.finishPasteLocked(r, errors.New("受管会话已结束，粘贴未发送"))
		}
	}
	value := s.titleContext
	value.Unknown = true
	s.setTitleContext(value)
	s.mu.Unlock()
	// 保留窗格和最后输出，显示断线提示；用户关闭窗格时才结束终端。
	s.display("\r\n[受管会话已结束：" + reason + "]\r\n")
	s.mu.Lock()
}

// Read 向终端转发远端 PTY 字节。
func (s *ManagedSession) Read(p []byte) (int, error) {
	return s.termRead.Read(p)
}

// Write 接收窗口键盘输入与终端协议应答，经控制通道转发到远端 PTY。
func (s *ManagedSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.disconnected {
		return 0, os.ErrClosed
	}
	if terminalReply.Match(p) || bytes.Equal(p, []byte("\x1b[I")) || bytes.Equal(p, []byte("\x1b[O")) {
		s.sendKeyboardLocked(p)
		return len(p), nil
	}
	s.pasteVersion++
	if s.blocksHumanInput() {
		if isInterrupt(p) {
			s.sendKeyboardLocked([]byte{3})
			return len(p), nil
		}
		s.inputBlocked = true
		return len(p), nil
	}
	if s.owner != ControlAgent && (s.active != nil || s.humanRunning) {
		s.sendKeyboardLocked(p)
		return len(p), nil
	}
	s.trackInputLocked(p)
	s.sendKeyboardLocked(p)
	return len(p), nil
}

func (s *ManagedSession) sendKeyboardLocked(p []byte) {
	s.sendMessageLocked(map[string]any{"t": "keyboard", "data": base64.StdEncoding.EncodeToString(p)}, nil)
}

func (s *ManagedSession) trackInputLocked(p []byte) {
	if match := kittyKey.FindSubmatch(p); len(match) != 0 {
		if string(match[1]) == "13" {
			s.pending++
			s.dirty = s.keys > s.submitted
		} else {
			s.dirty = true
		}
		return
	}
	for _, b := range p {
		switch b {
		case '\r', '\n':
			s.pending++
			s.dirty = s.keys > s.submitted
		case 3:
			s.dirty = false
		default:
			s.dirty = true
		}
	}
}

func (s *ManagedSession) blocksHumanInput() bool {
	return s.owner == ControlAgent
}

// Resize 把窗口尺寸同步到远端 PTY。
func (s *ManagedSession) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.disconnected {
		return os.ErrClosed
	}
	s.sendMessageLocked(map[string]any{"t": "resize", "cols": max(0, min(cols, 65535)), "rows": max(0, min(rows, 65535))}, nil)
	return nil
}

// OnKey 在入队前记录人工输入，并在 Agent 控制期间阻止按键。
func (s *ManagedSession) OnKey(mods ui.Modifiers, key ui.Key) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocksHumanInput() {
		if mods&ui.Ctrl != 0 && key == ui.KeyC {
			return false
		}
		s.inputBlocked = true
		return true
	}
	if s.owner != ControlAgent && (s.active != nil || s.humanRunning) {
		return false
	}
	if mods&ui.Super != 0 {
		return false
	}
	s.pasteVersion++
	s.keys++
	if key == ui.KeyEnter || (mods&ui.Ctrl != 0 && key == ui.KeyC) {
		s.submitted = s.keys
	} else {
		s.dirty = true
	}
	return false
}

// OnPaste 在人工粘贴前记录输入；Agent 控制期间不发送粘贴内容。
func (s *ManagedSession) OnPaste() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocksHumanInput() {
		s.inputBlocked = true
		return true
	}
	if s.owner != ControlAgent && (s.active != nil || s.humanRunning) {
		return false
	}
	s.pasteVersion++
	s.dirty = true
	return false
}

// Execute 在受管 Bash 就绪时提交命令，并等待完成通知或等待结束。
func (s *ManagedSession) Execute(ctx context.Context, command string) control.Result {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return control.Result{Err: errors.New("终端会话已结束")}
	}
	if s.disconnected {
		s.mu.Unlock()
		return control.Result{Err: errors.New("受管会话连接已失效，请显式新建受管会话；命令未发送")}
	}
	if s.owner == ControlHumanTakeover {
		s.mu.Unlock()
		return control.Result{Err: errors.New("终端已由人类接管，请等待交还 Agent")}
	}
	if command == "" {
		s.mu.Unlock()
		return control.Result{Err: errors.New("命令不能为空")}
	}
	if strings.IndexByte(command, 0) >= 0 {
		s.mu.Unlock()
		return control.Result{Err: errors.New("远端命令不能包含 NUL 字节")}
	}
	idle := s.bashReady || (s.owner == ControlHuman && s.humanPrompt)
	ready := idle && !s.dirty && s.pending == 0 && s.active == nil && s.paste == nil && len(s.unconfirmed) == 0
	if !ready {
		unknown := len(s.unconfirmed) != 0
		s.mu.Unlock()
		if unknown {
			return control.Result{Err: errors.New("此前命令结果未知，请在目标窗格接管并等待完成通知")}
		}
		return control.Result{Err: errors.New("终端尚未就绪或有人工输入、命令正在执行")}
	}
	output, err := os.CreateTemp(s.dir, "output-")
	if err != nil {
		s.mu.Unlock()
		return control.Result{Err: fmt.Errorf("创建输出文件失败: %w", err)}
	}
	s.nextID++
	r := &managedRequest{id: strconv.FormatUint(s.nextID, 10), command: command, output: output, done: make(chan struct{})}
	s.active = r
	fromHuman := s.humanPrompt
	s.bashReady = false
	s.humanPrompt = false
	s.stopIdle()
	s.setControl(ControlAgent)
	if fromHuman {
		s.sendMessageLocked(map[string]any{"t": "release"}, r)
	}
	s.sendMessageLocked(map[string]any{"t": "exec", "id": r.id, "cmd": command}, r)
	s.mu.Unlock()
	s.display("\r\n[CLI] " + sanitizeCommandText(command) + "\r\n")
	select {
	case <-r.done:
		return r.result
	case <-ctx.Done():
		s.mu.Lock()
		select {
		case <-r.done:
			result := r.result
			s.mu.Unlock()
			return result
		default:
		}
		r.abandoned = true
		if !r.sent {
			s.dropQueuedLocked(r)
			if s.active == r {
				s.active = nil
				s.state(false)
			}
			s.restoreIdleLocked()
			_ = r.output.Close()
			_ = os.Remove(r.output.Name())
			s.mu.Unlock()
			s.finish(r, 1, errors.New("执行已取消，命令未发送"))
			return control.Result{Err: errors.New("执行已取消，命令未发送")}
		}
		if s.active == r {
			s.active = nil
			s.state(false)
			s.unconfirmed[r] = struct{}{}
		}
		_ = r.output.Close()
		_ = os.Remove(r.output.Name())
		s.mu.Unlock()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return control.Result{Err: errors.New("执行超时，结果未知，命令可能仍在执行")}
		}
		return control.Result{Err: errors.New("执行已中断，结果未知，命令可能仍在执行")}
	}
}

func (s *ManagedSession) dropQueuedLocked(r *managedRequest) {
	out := s.out[:0]
	for _, m := range s.out {
		if m.req != r {
			out = append(out, m)
		}
	}
	s.out = out
}

func (s *ManagedSession) dropQueuedPasteLocked(r *managedPaste) {
	out := s.out[:0]
	for _, m := range s.out {
		if m.paste != r {
			out = append(out, m)
		}
	}
	s.out = out
}

func (s *ManagedSession) restoreIdleLocked() {
	if s.owner == ControlAgent {
		s.bashReady = s.active == nil
		if s.bashReady {
			s.startIdle()
		}
	}
}

// finish 由持有 s.mu 的调用方调用；幂等，首个结果生效。
func (s *ManagedSession) finish(r *managedRequest, code int, err error) {
	select {
	case <-r.done:
		return
	default:
	}
	if !r.abandoned {
		_, seekErr := r.output.Seek(0, io.SeekStart)
		r.result = control.Result{Output: r.output, ExitCode: code, Err: errors.Join(err, seekErr)}
	}
	close(r.done)
}

// Close 结束受管会话；只释放本会话的通道，不影响复用连接。
// 尚未发送的请求明确不执行；已出队写入的请求结果未知。
func (s *ManagedSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.stopIdle()
		kept := s.out[:0]
		for _, m := range s.out {
			switch {
			case m.req != nil:
				r := m.req
				if s.active == r {
					s.active = nil
					s.state(false)
				}
				select {
				case <-r.done:
				default:
					s.finish(r, 1, errors.New("窗格已关闭，命令未执行"))
				}
			case m.paste != nil:
				s.finishPasteLocked(m.paste, errors.New("目标窗格已关闭，粘贴未发送"))
			default:
				// 未发送的键盘/尺寸等消息不再写入。
			}
		}
		s.out = kept
		if r := s.active; r != nil {
			s.active = nil
			s.state(false)
			s.finish(r, 1, errors.New("窗格已关闭，命令结果未知"))
		}
		if r := s.paste; r != nil {
			if r.sent {
				s.finishPasteLocked(r, control.ErrPasteUnknown)
			} else {
				s.finishPasteLocked(r, errors.New("目标窗格已关闭，粘贴未发送"))
			}
		}
		s.enqueueLocked(managedOut{close: true})
		s.mu.Unlock()
		go func() {
			select {
			case <-s.readerDone:
			case <-time.After(3 * time.Second):
				// 终端可能已经停止读取，先解开向终端缓冲写入的阻塞。
				_ = s.termWrite.Close()
				if s.cmd.Process != nil {
					_ = s.cmd.Process.Kill()
				}
				<-s.readerDone
			}
			_ = s.termWrite.Close()
			_ = s.termRead.Close()
		}()
	})
	return nil
}

// Done 在会话资源回收后关闭。
func (s *ManagedSession) Done() <-chan struct{} { return s.readerDone }
