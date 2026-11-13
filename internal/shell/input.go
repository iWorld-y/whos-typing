// 本文件协调窗口输入、CLI 请求和伪终端尺寸。
package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/creack/pty"
	"github.com/egoist/mygo/ui"
	"whos-typing/internal/control"
)

// SetSender 设置 MyGo 终端的输入队列，须在接收 CLI 请求前调用。
func (s *Session) SetSender(send func([]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.send = send
}

// SetStateHandler 设置 CLI 执行状态回调；回调不得等待 Shell 或窗口线程。
func (s *Session) SetStateHandler(fn func(bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onState = fn
}

func (s *Session) state(active bool) {
	if s.onState != nil {
		s.onState(active)
	}
}

// OnKey 在 MyGo 入队前记录人工输入，并在 CLI 执行期间阻止按键。
func (s *Session) OnKey(mods ui.Modifiers, key ui.Key) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocksHumanInput() {
		if mods&ui.Ctrl != 0 && key == ui.KeyC {
			return false
		}
		s.inputBlocked = true
		return true
	}
	if s.humanForegroundInput() {
		return false
	}
	if mods&ui.Super != 0 {
		return false
	}
	s.keys++
	if key == ui.KeyEnter || (mods&ui.Ctrl != 0 && (key == ui.KeyC || (s.remote != nil && key == ui.KeyD))) {
		s.submitted = s.keys
	} else {
		s.dirty = true
		if s.remote != nil && !s.remote.attaching {
			s.remote.dirty = true
		}
	}
	return false
}

// OnPaste 在人工粘贴前记录输入；CLI 执行期间不发送粘贴内容。
func (s *Session) OnPaste() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocksHumanInput() {
		s.inputBlocked = true
		return true
	}
	if s.humanForegroundInput() {
		return false
	}
	s.dirty = true
	if s.remote != nil && !s.remote.attaching {
		s.remote.dirty = true
	}
	return false
}

// Execute 在当前 Shell 中执行命令，并等待完成或会话结束。
func (s *Session) Execute(ctx context.Context, command string) control.Result {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return control.Result{Err: errors.New("终端会话已结束")}
	}
	if s.owner == ControlHumanTakeover {
		s.mu.Unlock()
		return control.Result{Err: errors.New("终端已由人类接管，请等待交还 Agent")}
	}
	remote := s.remote != nil && !s.remote.attaching
	if s.remote != nil && s.remote.attaching {
		s.mu.Unlock()
		return control.Result{Err: errors.New("远端接入尚未完成")}
	}
	if !remote && !s.supported() {
		s.mu.Unlock()
		return control.Result{Err: fmt.Errorf("当前 Shell %q 不支持 CLI 执行", s.kind)}
	}
	ready := s.ready && !s.dirty && s.pending == 0
	if remote {
		ready = s.remote.ready && len(s.remote.idleOutput) == 0 && !s.remote.dirty && s.remote.pending == 0 && s.remote.orphan == nil
		if strings.IndexByte(command, 0) >= 0 {
			s.mu.Unlock()
			return control.Result{Err: errors.New("远端命令不能包含 NUL 字节")}
		}
	}
	if s.send == nil || !ready || s.queued != nil || s.active != nil {
		unknown := remote && (s.remote.orphan != nil || len(s.unconfirmed) != 0)
		s.mu.Unlock()
		if unknown {
			return control.Result{Err: errors.New("远端命令结果未知，请接管后使用恢复远端接入")}
		}
		if remote {
			return control.Result{Err: errors.New("远端 Shell 尚未就绪，请完成当前输入或等待当前命令；若接入失效，请接管后使用恢复远端接入")}
		}
		return control.Result{Err: errors.New("终端尚未就绪或有人工输入、命令正在执行")}
	}
	sourceName := ""
	if !remote {
		source, err := os.CreateTemp(s.dir, "command-")
		if err != nil {
			s.mu.Unlock()
			return control.Result{Err: fmt.Errorf("创建命令文件失败: %w", err)}
		}
		sourceName = source.Name()
		_, writeErr := io.WriteString(source, command)
		closeErr := source.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			_ = os.Remove(sourceName)
			s.mu.Unlock()
			return control.Result{Err: fmt.Errorf("写入命令失败: %w", err)}
		}
	}
	output, err := os.CreateTemp(s.dir, "output-")
	if err != nil {
		_ = os.Remove(sourceName)
		s.mu.Unlock()
		return control.Result{Err: fmt.Errorf("创建输出文件失败: %w", err)}
	}
	r := &request{command: command, source: sourceName, output: output, done: make(chan struct{}), remote: remote, humanBefore: s.owner == ControlHuman}
	if remote {
		r.remoteBinding = s.remote
	}
	r.barrier = []byte("\x00WT-REQUEST:" + s.token + ":" + filepath.Base(output.Name()))
	s.queued = r
	s.stopIdle()
	s.setControl(ControlAgent)
	s.send(r.barrier)
	s.mu.Unlock()
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
		if r.remoteBinding != nil {
			r.remoteBinding.ready = false
		}
		if s.queued == r {
			s.queued = nil
			_ = os.Remove(r.source)
			s.restoreQueuedControl(r)
		}
		if s.active == r && !r.finished {
			s.markUnknown(r)
		}
		if r.remoteBinding != nil && s.active == r {
			r.remoteBinding.orphan = r
			s.active = nil
			s.state(false)
			if r.injecting {
				r.resetWriteDeadline = true
				_ = s.pty.SetWriteDeadline(time.Now())
			}
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

// Write 接收窗口输入，也处理与窗口输入有序的 CLI 请求。
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bytes.HasPrefix(p, []byte("\x00WT-ATTACH:"+s.token+":")) {
		r := s.remote
		if r == nil || !r.attaching || !bytes.Equal(p, r.auto) {
			return len(p), nil
		}
		r.auto = nil
		if err := r.autoCtx.Err(); err != nil {
			s.failAttach(r, err)
			return len(p), nil
		}
		if err := s.checkAutoAttach(r); err != nil {
			s.failAttach(r, err)
			return len(p), nil
		}
		if s.owner == ControlHumanTakeover && !r.recovery {
			s.failAttach(r, errors.New("终端已由人类接管，远端接入未发送"))
			return len(p), nil
		}
		if r.recovery {
			if (s.active != nil && s.active.injecting) || (r.previous.orphan != nil && r.previous.orphan.injecting) {
				s.failAttach(r, errors.New("命令输入仍在发送，请稍后恢复远端接入"))
				return len(p), nil
			}
			s.abandonRemoteRequests(r.previous)
		}
		s.stopIdle()
		if !r.recovery {
			s.setControl(ControlAgent)
		}
		payload := []byte(r.bootstrap + "\r")
		r.input = append([]byte(nil), payload...)
		r.injected = true
		r.injecting = true
		s.state(true)
		s.parsed = append(s.parsed, []byte("\r\n[CLI] 正在接入远端 Bash\r\n")...)
		s.mu.Unlock()
		s.writeMu.Lock()
		n, err := s.pty.Write(payload)
		s.mu.Lock()
		r.injecting = false
		if r.resetWrite {
			_ = s.pty.SetWriteDeadline(time.Time{})
		}
		s.writeMu.Unlock()
		if err == nil && n != len(payload) {
			err = io.ErrShortWrite
		}
		if err != nil && s.remote == r {
			s.failAttach(r, fmt.Errorf("发送远端接入命令失败，结果未知: %w", err))
		}
		return len(p), nil
	}
	if bytes.HasPrefix(p, []byte("\x00WT-INJECT:"+s.token+":")) {
		r := s.active
		if r == nil || !r.remote || !bytes.Equal(p, r.inject) {
			return len(p), nil
		}
		line := p[len("\x00WT-INJECT:"+s.token+":"+filepath.Base(r.output.Name())+":"):]
		r.inject = nil
		_, err := s.writeAgentPTY(r, line)
		if err != nil && s.active == r {
			s.active = nil
			r.remoteBinding.ready = false
			r.remoteBinding.orphan = r
			s.markUnknown(r)
			s.state(false)
			s.finish(r, 1, fmt.Errorf("发送远端命令失败，结果未知: %w", err))
		}
		return len(p), nil
	}
	if bytes.HasPrefix(p, []byte("\x00WT-REQUEST:"+s.token+":")) {
		r := s.queued
		if r == nil || !bytes.Equal(p, r.barrier) {
			return len(p), nil
		}
		s.queued = nil
		ready := s.ready && !s.dirty && s.pending == 0
		if r.remote {
			ready = s.remote == r.remoteBinding && s.remote != nil && !s.remote.attaching && s.remote.ready && len(s.remote.idleOutput) == 0 && !s.remote.dirty && s.remote.pending == 0 && s.remote.orphan == nil
		}
		if s.closed || !ready || s.active != nil {
			s.restoreQueuedControl(r)
			s.finish(r, 1, errors.New("终端有人工输入或命令正在执行"))
			return len(p), nil
		}
		s.active = r
		if r.remote {
			s.remote.ready = false
		} else {
			s.ready = false
		}
		s.state(true)
		if r.remote {
			line, err := remoteCommand(s.remote.token, r.command)
			if err != nil {
				s.active = nil
				s.state(false)
				s.finish(r, 1, err)
				return len(p), nil
			}
			prefix := "\x00WT-INJECT:" + s.token + ":" + filepath.Base(r.output.Name()) + ":"
			r.inject = append([]byte(prefix), []byte(line)...)
			s.send(r.inject)
			return len(p), nil
		}
		path := s.quote(r.source)
		line := fmt.Sprintf("%s %s; source %s; %s $?\r", s.begin, path, path, s.end)
		if s.kind == "fish" {
			command := "__wt_command_" + s.token
			restore := "__wt_restore_" + s.token
			line = fmt.Sprintf("%s; set -g %s (string collect -N < %s); %s; eval \"$%s\"; %s $status\r", s.begin, command, path, restore, command, s.end)
		}
		if _, err := s.writeAgentPTY(r, []byte(line)); err != nil && s.active == r {
			s.active = nil
			s.localOrphan = r
			s.markUnknown(r)
			s.state(false)
			s.finish(r, 1, fmt.Errorf("发送命令失败，结果未知: %w", err))
		}
		return len(p), nil
	}
	if s.closed {
		return 0, os.ErrClosed
	}
	if terminalReply.Match(p) {
		return s.writePTY(p)
	}
	// 屏障前已排队的人工输入应先处理，再决定能否执行 CLI 命令。
	if s.blocksHumanInput() && (s.queued == nil || !s.queued.humanBefore) {
		if isInterrupt(p) {
			remote := s.remote
			_, err := s.writePTY([]byte{3})
			if remote != nil && s.remote == remote && remote.attaching && remote.injected {
				s.failAttach(remote, errors.New("远端接入已中断，接入命令可能已执行，请确认远端状态"))
			}
			return len(p), err
		}
		// 输入法和终端协议回复可能没有 OnKey，不能回放到 Shell。
		s.inputBlocked = true
		return len(p), nil
	}
	if s.humanForegroundInput() {
		if r := s.remote; r != nil && r.attaching && r.injected {
			r.input = append(r.input, p...)
		}
		return s.writePTY(p)
	}
	if r := s.remote; r != nil {
		if r.attaching {
			if len(r.input) <= len(r.bootstrap)+1024 {
				r.input = append(r.input, p...)
			}
		} else {
			r.trackInput(s, p)
			r.ready = false
		}
	}
	s.trackInput(p)
	return s.writePTY(p)
}

// writePTY 写入期间释放状态锁，让输出读取和取消操作能够继续。
func (s *Session) writePTY(p []byte) (int, error) {
	s.mu.Unlock()
	s.writeMu.Lock()
	n, err := s.pty.Write(p)
	s.writeMu.Unlock()
	s.mu.Lock()
	return n, err
}

// writeAgentPTY 允许接管或取消唤醒阻塞的 Agent 写入。
func (s *Session) writeAgentPTY(r *request, p []byte) (int, error) {
	r.injecting = true
	s.mu.Unlock()
	s.writeMu.Lock()
	n, err := s.pty.Write(p)
	s.mu.Lock()
	r.injecting = false
	if r.resetWriteDeadline {
		_ = s.pty.SetWriteDeadline(time.Time{})
		r.resetWriteDeadline = false
	}
	s.writeMu.Unlock()
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// 这些回复由终端产生，和键盘或粘贴输入分开处理。
var terminalReply = regexp.MustCompile(`^\x1b(?:\[(?:\?[0-9;:]+[cu]|>[0-9;:]+[cq]|[0-9;:]+[nR])|\](?:10|11|12);[^\x07\x1b]*(?:\x07|\x1b\\)|P[^\x1b]*\x1b\\)$`)

var kittyKey = regexp.MustCompile(`^\x1b\[([0-9]+)(?:;([0-9]+)(?::[0-9]+)?)?u$`)

func isInterrupt(p []byte) bool {
	if bytes.Equal(p, []byte{3}) {
		return true
	}
	match := kittyKey.FindSubmatch(p)
	if len(match) == 0 || (string(match[1]) != "99" && string(match[1]) != "67") {
		return false
	}
	mods, err := strconv.Atoi(string(match[2]))
	return err == nil && (mods-1)&4 != 0
}

func (s *Session) trackInput(p []byte) {
	if match := kittyKey.FindSubmatch(p); len(match) != 0 {
		if string(match[1]) == "13" {
			s.pending++
			s.dirty = s.keys > s.submitted
			s.ready = false
		} else {
			s.dirty = true
		}
		return
	}
	for _, b := range p {
		switch b {
		case '\r', '\n', 3:
			s.pending++
			s.dirty = s.keys > s.submitted
			s.ready = false
		default:
			s.dirty = true
		}
	}
}

// Resize 将窗口尺寸同步到伪终端。
func (s *Session) Resize(cols, rows int) error {
	conn, err := s.pty.SyscallConn()
	if err != nil {
		return err
	}
	size := pty.Winsize{Cols: uint16(max(0, min(cols, 65535))), Rows: uint16(max(0, min(rows, 65535)))}
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(&size)))
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}
