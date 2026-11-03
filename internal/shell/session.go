// Package shell 将一个本地 Shell 连接到窗口和命令执行入口。
package shell

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"whos-typing/internal/control"
)

type request struct {
	barrier            []byte
	command            string
	source             string
	output             *os.File
	started            bool
	finished           bool
	exitCode           int
	abandoned          bool
	err                error
	done               chan struct{}
	result             control.Result
	capture            []byte
	prefix             []byte
	remote             bool
	humanBefore        bool // 排队前由人类控制，屏障之前的输入仍须处理。
	inject             []byte
	injecting          bool
	resetWriteDeadline bool
}

// Session 持有窗口所用的伪终端，并在同一个 Shell 中执行 CLI 命令。
type Session struct {
	pty          *os.File
	command      *exec.Cmd
	kind         string
	dir          string
	token        string
	begin        string
	end          string
	initPath     string
	marker       []byte
	initLine     []byte
	initScan     []byte
	initSeen     bool
	readBuf      []byte
	discardTitle bool
	parsed       []byte
	exited       chan struct{}
	exitCode     int

	mu           sync.Mutex
	writeMu      sync.Mutex
	send         func([]byte)
	onState      func(bool)
	owner        ControlOwner
	onControl    func(ControlOwner)
	titleContext TitleContext
	localTitle   *TitleContext
	onTitle      func()
	idleStarted  time.Time
	idleVersion  uint64
	idleCancel   func()
	idleNow      func() time.Time
	idleAfter    func(time.Duration, func()) func()
	initial      bool
	ready        bool
	dirty        bool
	pending      int
	keys         uint64
	submitted    uint64
	queued       *request
	active       *request
	localOrphan  *request
	unconfirmed  map[*request]struct{}
	inputBlocked bool
	remote       *remoteSession
	retired      []*remoteSession
	closed       bool
	closeOnce    sync.Once
}

// New 启动用户登录 Shell；dir 用于存放本次会话的临时文件。
func New(dir string) (*Session, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("生成会话标识失败: %w", err)
	}
	token := hex.EncodeToString(random[:])
	name := os.Getenv("SHELL")
	if name == "" {
		name = "/bin/sh"
		if runtime.GOOS == "darwin" {
			name = "/bin/zsh"
		}
	}
	s := &Session{
		kind: filepath.Base(name), dir: dir, token: token,
		begin: "__wt_begin_" + token, end: "__wt_end_" + token,
		marker: []byte("\x1eWT:" + token + ":"), exited: make(chan struct{}), titleContext: InitialTitleContext(),
	}
	if s.supported() {
		if err := s.prepareInit(); err != nil {
			return nil, err
		}
	}
	s.initSeen = !s.supported() || s.kind == "fish"
	if !s.initSeen {
		s.initLine = []byte("source " + s.quote(s.initPath))
	}
	cmd := exec.Command(name)
	cmd.Args[0] = "-" + filepath.Base(name)
	if s.kind == "fish" {
		cmd.Args = append(cmd.Args, "--init-command", "source "+s.quote(s.initPath))
	}
	cmd.Dir, _ = os.UserHomeDir()
	cmd.Env = terminalEnvironment()
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		_ = os.Remove(s.initPath)
		return nil, fmt.Errorf("启动 Shell 失败: %w", err)
	}
	pollable, err := pollableMaster(master)
	if err != nil {
		_ = master.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = os.Remove(s.initPath)
		return nil, fmt.Errorf("配置终端读取失败: %w", err)
	}
	s.pty, s.command = pollable, cmd
	go s.wait()
	if s.supported() && s.kind != "fish" {
		if _, err := io.WriteString(s.pty, "source "+s.quote(s.initPath)+"\r"); err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("初始化 Shell 失败: %w", err)
		}
	}
	return s, nil
}

// pollableMaster 复制 PTY 到非阻塞 fd，使 Go 能在关闭时取消阻塞读取。
func pollableMaster(master *os.File) (*os.File, error) {
	raw, err := master.SyscallConn()
	if err != nil {
		return nil, err
	}
	duplicate := -1
	var setupErr error
	if err := raw.Control(func(fd uintptr) {
		copyFD, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, uintptr(syscall.F_DUPFD_CLOEXEC), 0)
		if errno != 0 {
			setupErr = errno
			return
		}
		duplicate = int(copyFD)
		setupErr = syscall.SetNonblock(duplicate, true)
	}); err != nil {
		if duplicate >= 0 {
			_ = syscall.Close(duplicate)
		}
		return nil, err
	}
	if setupErr != nil {
		if duplicate >= 0 {
			_ = syscall.Close(duplicate)
		}
		return nil, setupErr
	}
	file := os.NewFile(uintptr(duplicate), "pty-master")
	if file == nil {
		_ = syscall.Close(duplicate)
		return nil, errors.New("无法创建 PTY 文件")
	}
	if err := master.Close(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func terminalEnvironment() []string {
	env := os.Environ()
	set := func(name, value string) {
		prefix := name + "="
		for i, item := range env {
			if strings.HasPrefix(item, prefix) {
				env[i] = prefix + value
				return
			}
		}
		env = append(env, prefix+value)
	}
	set("TERM", "xterm-256color")
	set("COLORTERM", "truecolor")
	if os.Getenv("LANG") == "" && os.Getenv("LC_ALL") == "" && os.Getenv("LC_CTYPE") == "" {
		set("LANG", "en_US.UTF-8")
	}
	return env
}

func (s *Session) supported() bool {
	return s.kind == "fish" || s.kind == "zsh" || s.kind == "bash"
}

func (s *Session) prepareInit() error {
	f, err := os.CreateTemp(s.dir, "init-")
	if err != nil {
		return fmt.Errorf("创建 Shell 初始化文件失败: %w", err)
	}
	s.initPath = f.Name()
	_, writeErr := io.WriteString(f, s.initScript())
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(s.initPath)
		return fmt.Errorf("写入 Shell 初始化文件失败: %w", err)
	}
	return nil
}

func (s *Session) quote(value string) string {
	if s.kind == "fish" {
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, `'`, `\'`)
	} else {
		value = strings.ReplaceAll(value, `'`, `'\''`)
	}
	return "'" + value + "'"
}

func (s *Session) initScript() string {
	prompt := "__wt_prompt_" + s.token
	mark := "\\036WT:" + s.token + ":"
	if s.kind == "fish" {
		previous := "__wt_previous_" + s.token
		command := "__wt_command_" + s.token
		restore := "__wt_restore_" + s.token
		return fmt.Sprintf(`function %s --on-event fish_prompt
  set -l saved_status $status
  %s
  printf '%sP\037'
  return $saved_status
end
function %s
  set -g %s $status
  printf '%sS\037'
  return $%s
end
function %s
  return $%s
end
function %s
  set -l saved_status $argv[1]
  set -e %s %s
  printf '%sE:%%s\037' "$saved_status"
  return $saved_status
end
printf '%sI\037'
`, prompt, titlePrompt(mark, true), mark, s.begin, previous, mark, previous, restore, previous, s.end, command, previous, mark, mark)
	}
	setup := "precmd_functions+=(" + prompt + ")"
	if s.kind == "bash" {
		setup = fmt.Sprintf(`case "$(declare -p PROMPT_COMMAND 2>/dev/null)" in
  'declare -a '*) PROMPT_COMMAND+=(%s) ;;
  *) PROMPT_COMMAND="${PROMPT_COMMAND:+${PROMPT_COMMAND}; }%s" ;;
esac`, prompt, prompt)
	}
	return fmt.Sprintf(`%s() { local saved_status=$?; %s printf '%sP\037'; return "$saved_status"; }
%s() { local saved_status=$?; printf '%sS\037'; return "$saved_status"; }
%s() { local saved_status=$1; printf '%sE:%%s\037' "$saved_status"; return "$saved_status"; }
%s
printf '%sI\037'
`, prompt, titlePrompt(mark, false), mark, s.begin, mark, s.end, mark, setup, mark)
}

func (s *Session) wait() {
	err := s.command.Wait()
	code := 0
	if err != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				code = 128 + int(status.Signal())
			}
		}
	}
	s.mu.Lock()
	s.exitCode = code
	s.mu.Unlock()
	close(s.exited)
	// 子进程可能仍持有终端，保留短暂时间读取 Shell 已写出的数据。
	time.Sleep(100 * time.Millisecond)
	_ = s.pty.Close()
}

func (s *Session) onEnd() {
	select {
	case <-s.exited:
	case <-time.After(2 * time.Second):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remote != nil && s.remote.attaching {
		s.failAttach(s.remote, errors.New("终端会话已结束，远端接入未完成"))
	}
	if s.queued != nil {
		s.finish(s.queued, 1, errors.New("Shell 在执行前退出"))
		s.queued = nil
	}
	if r := s.active; r != nil {
		s.captureOutput(r, nil, true)
		err := errors.Join(r.err, errors.New("Shell 已退出，未报告命令完成状态"))
		s.finish(r, s.exitCode, err)
		s.active = nil
		s.state(false)
	}
}

// Close 关闭伪终端并结束会话，重复调用安全。
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.stopIdle()
		s.mu.Unlock()
		err = s.pty.Close()
		if s.initPath != "" {
			_ = os.Remove(s.initPath)
		}
		go func() {
			select {
			case <-s.exited:
			case <-time.After(2 * time.Second):
				_ = s.command.Process.Kill()
			}
		}()
	})
	return err
}
