package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/creack/pty"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type fixture struct {
	s       *Session
	gui     *safeBuffer
	input   chan []byte
	readEnd chan struct{}
	term    *terminal.Terminal
}

type terminalResponder struct {
	s       *Session
	seen    []byte
	replied bool
}

func (r *terminalResponder) Write(p []byte) (int, error) {
	r.seen = append(r.seen, p...)
	if !r.replied && bytes.Contains(r.seen, []byte("\x1b[0c")) {
		r.replied = true
		_, _ = r.s.Write([]byte("\x1b[?1;2c"))
	}
	return len(p), nil
}

func newFixture(t *testing.T, shellPath string) *fixture {
	t.Helper()
	useNative := filepath.Base(shellPath) == "fish"
	if useNative && os.Getenv("MYGO_GHOSTTY_VT") == "" {
		t.Skip("fish 测试需要 MYGO_GHOSTTY_VT 指向上游原生库")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	if useNative {
		cache := filepath.Join(home, "cache")
		t.Setenv("XDG_CACHE_HOME", cache)
		if err := os.MkdirAll(filepath.Join(cache, "fish", "generated_completions"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", shellPath)
	dir := filepath.Join(t.TempDir(), "it's a runtime")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{s: s, gui: &safeBuffer{}, input: make(chan []byte, 32), readEnd: make(chan struct{})}
	if useNative {
		term, err := terminal.New(terminal.Options{
			Conn: s, OnKey: s.OnKey,
			OnPaste: func(transfer.Data) bool { return s.OnPaste() },
		})
		if err != nil {
			_ = s.Close()
			t.Fatalf("创建 MyGo 终端失败: %v", err)
		}
		f.term = term
		s.SetSender(term.Send)
		go func() { <-term.Done(); close(f.readEnd) }()
	} else {
		s.SetSender(func(data []byte) { f.input <- append([]byte(nil), data...) })
		go func() {
			for p := range f.input {
				_, _ = s.Write(p)
			}
		}()
		go func() {
			_, _ = io.Copy(io.MultiWriter(f.gui, &terminalResponder{s: s}), s)
			close(f.readEnd)
		}()
	}
	t.Cleanup(func() {
		if f.term != nil {
			_ = f.term.Close()
		} else {
			_ = s.Close()
		}
		close(f.input)
		select {
		case <-f.readEnd:
		case <-time.After(4 * time.Second):
			t.Error("等待终端读取结束超时")
		}
	})
	f.waitReady(t)
	return f
}

func (f *fixture) screen() string {
	if f.term != nil {
		return f.term.Text()
	}
	return f.gui.String()
}

func (f *fixture) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		f.s.mu.Lock()
		ready := f.s.ready && f.s.pending == 0 && !f.s.dirty && f.s.active == nil
		f.s.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Shell 未就绪。GUI 输出: %q", f.screen())
}

func waitForegroundGroup(t *testing.T, s *Session) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := s.pty.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var group int32
		var errno syscall.Errno
		if err := raw.Control(func(fd uintptr) {
			_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&group)))
		}); err != nil || errno != 0 {
			t.Fatalf("读取前台进程组失败: %v %v", err, errno)
		}
		if group > 1 && int(group) != s.command.Process.Pid {
			return int(group)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("测试命令未进入前台进程组")
	return 0
}

func (f *fixture) run(t *testing.T, command string) (string, int, error) {
	t.Helper()
	t.Logf("执行命令: %s", command)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result := f.s.Execute(ctx, command)
	if result.Output == nil {
		return "", result.ExitCode, result.Err
	}
	defer os.Remove(result.Output.Name())
	defer result.Output.Close()
	data, err := io.ReadAll(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), result.ExitCode, result.Err
}

func TestRealShellCommands(t *testing.T) {
	for _, shellPath := range []string{"/opt/homebrew/bin/fish", "/bin/zsh", "/bin/bash"} {
		t.Run(filepath.Base(shellPath), func(t *testing.T) {
			if _, err := os.Stat(shellPath); err != nil {
				t.Skipf("Shell 不可用: %v", err)
			}
			f := newFixture(t, shellPath)
			for i, tc := range []struct {
				command string
				want    string
				code    int
			}{
				{"", "", 0},
				{"printf 'one\\n'; printf 'two\\n'", "one\r\ntwo\r\n", 0},
				{"printf \"a'b\\n\"\nprintf 'second\\n'", "a'b\r\nsecond\r\n", 0},
				{"printf 'error' >&2; false", "error", 1},
				{"printf '\\000X'", "\x00X", 0},
				{"true", "", 0},
				{"cd /tmp", "", 0},
				{"pwd -P", "/private/tmp\r\n", 0},
			} {
				output, code, err := f.run(t, tc.command)
				if err != nil || code != tc.code || output != tc.want {
					t.Fatalf("命令 %q: 输出 %q，退出码 %d，错误 %v", tc.command, output, code, err)
				}
				if i == 1 {
					deadline := time.Now().Add(time.Second)
					for time.Now().Before(deadline) && !strings.Contains(f.screen(), "two") {
						time.Sleep(10 * time.Millisecond)
					}
					gui := f.screen()
					for _, hidden := range []string{f.s.begin, f.s.end, string(f.s.marker), f.s.initPath, "init-"} {
						if at := strings.Index(gui, hidden); at >= 0 {
							t.Fatalf("GUI 包含内部内容 %q，附近内容 %q", hidden, gui[max(0, at-120):min(len(gui), at+120)])
						}
					}
					if !strings.Contains(gui, "printf 'one\\n'; printf 'two\\n'") || !strings.Contains(strings.ReplaceAll(gui, "\r\n", "\n"), "one\ntwo") {
						t.Fatalf("GUI 未显示 CLI 命令和输出: %q", gui)
					}
				}
			}
			if output, code, err := f.run(t, "false"); err != nil || code != 1 || output != "" {
				t.Fatalf("false: %q %d %v", output, code, err)
			}
			status := `printf '%s' "$?"`
			if filepath.Base(shellPath) == "fish" {
				status = `printf '%s' "$status"`
			}
			if output, code, err := f.run(t, status); err != nil || code != 0 || output != "1" {
				t.Fatalf("上一条退出状态未保留: %q %d %v", output, code, err)
			}
			set := "export WT_SHARED=hello"
			if filepath.Base(shellPath) == "fish" {
				set = "set -gx WT_SHARED hello"
			}
			if output, code, err := f.run(t, set); err != nil || code != 0 || output != "" {
				t.Fatalf("设置变量失败: %q %d %v", output, code, err)
			}
			if output, code, err := f.run(t, "printf '%s' \"$WT_SHARED\""); err != nil || code != 0 || output != "hello" {
				t.Fatalf("变量未保留: %q %d %v", output, code, err)
			}
			local := "WT_LOCAL=local"
			if filepath.Base(shellPath) == "fish" {
				local = "set WT_LOCAL local"
			}
			if output, code, err := f.run(t, local); err != nil || code != 0 || output != "" {
				t.Fatalf("设置 Shell 变量失败: %q %d %v", output, code, err)
			}
			if output, code, err := f.run(t, "printf '%s' \"$WT_LOCAL\""); err != nil || code != 0 || output != "local" {
				t.Fatalf("Shell 变量未保留: %q %d %v", output, code, err)
			}
			output, code, err := f.run(t, "head -c 1048576 /dev/zero | tr '\\000' x")
			if err != nil || code != 0 || len(output) != 1048576 || strings.Trim(output, "x") != "" {
				t.Fatalf("大输出错误: 长度 %d，退出码 %d，错误 %v", len(output), code, err)
			}
		})
	}
}

func TestManualInputAndConcurrentCommands(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	f.s.OnKey(0, ui.KeyE)
	if _, err := f.s.Write([]byte("echo pending")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.run(t, "printf injected"); err == nil {
		t.Fatal("有未提交的人工输入时接受了 CLI 命令")
	}
	f.s.OnKey(0, ui.KeyEnter)
	if _, err := f.s.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	f.waitReady(t)
	if !strings.Contains(f.gui.String(), "pending") {
		t.Fatal("人工输入未保留")
	}
	result := make(chan error, 1)
	go func() {
		_, _, err := f.run(t, "sleep 1; printf finished")
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.s.mu.Lock()
		active := f.s.active != nil
		f.s.mu.Unlock()
		if active {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, err := f.run(t, "printf second"); err == nil {
		t.Fatal("并发 CLI 命令未被拒绝")
	}
	if !f.s.OnKey(0, ui.KeyE) {
		t.Fatal("CLI 执行期间未阻止键盘输入")
	}
	if _, err := f.s.Write([]byte("echo SHOULD_NOT_RUN\r")); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	f.waitReady(t)
	if strings.Contains(f.gui.String(), "SHOULD_NOT_RUN") {
		t.Fatal("被阻止的输入被回放到 Shell")
	}
}

func TestIncompleteShellExit(t *testing.T) {
	for _, shellPath := range []string{"/bin/zsh", "/opt/homebrew/bin/fish"} {
		t.Run(filepath.Base(shellPath), func(t *testing.T) {
			f := newFixture(t, shellPath)
			output, _, err := f.run(t, "printf before; exit 0")
			if err == nil || !strings.HasPrefix(output, "before") {
				t.Fatalf("缺少完成标记时的结果: 输出 %q，错误 %v", output, err)
			}
		})
	}
}

func TestRemovedPromptHookDoesNotHang(t *testing.T) {
	f := newFixture(t, "/bin/bash")
	start := time.Now()
	output, code, err := f.run(t, "unset PROMPT_COMMAND")
	if err != nil || code != 0 || output != "" || time.Since(start) > 4*time.Second {
		t.Fatalf("移除提示符钩子后没有返回已完成命令: %q %d %v", output, code, err)
	}
	if _, _, err := f.run(t, "printf must-not-run"); err == nil {
		t.Fatal("无法确认提示符时接受了新的命令")
	}
}

func TestWindowCloseDuringCommand(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	result := make(chan error, 1)
	go func() {
		_, _, err := f.run(t, "sleep 60")
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.s.mu.Lock()
		started := f.s.active != nil && f.s.active.started
		f.s.mu.Unlock()
		if started {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = f.s.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("窗口关闭后命令被报告为成功")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("窗口关闭后请求未结束")
	}
}

func TestShellPTYSupportsReadDeadline(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	if err := f.s.Resize(90, 30); err != nil {
		t.Fatalf("调整终端尺寸失败: %v", err)
	}
	if err := f.s.pty.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("PTY 不支持可取消的读取: %v", err)
	}
}

func TestPollableMasterDeadlineAndClose(t *testing.T) {
	for _, closeRead := range []bool{false, true} {
		t.Run(fmt.Sprintf("close=%v", closeRead), func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer slave.Close()
			file, err := pollableMaster(master)
			if err != nil {
				_ = master.Close()
				t.Fatal(err)
			}
			defer file.Close()
			raw, err := file.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var flags uintptr
			var errno syscall.Errno
			if err := raw.Control(func(fd uintptr) {
				flags, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, uintptr(syscall.F_GETFD), 0)
			}); err != nil || errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
				t.Fatalf("PTY 未设置 close-on-exec: flags=%d error=%v errno=%v", flags, err, errno)
			}
			if !closeRead {
				if err := file.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
					t.Fatalf("设置读取期限失败: %v", err)
				}
				_, err := file.Read(make([]byte, 1))
				if !errors.Is(err, os.ErrDeadlineExceeded) {
					t.Fatalf("阻塞读取未按期限结束: %v", err)
				}
				return
			}
			result := make(chan error, 1)
			go func() {
				_, err := file.Read(make([]byte, 1))
				result <- err
			}()
			select {
			case err := <-result:
				t.Fatalf("关闭前读取不应返回: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if err := file.Close(); err != nil {
				t.Fatalf("关闭 PTY 失败: %v", err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, os.ErrClosed) {
					t.Fatalf("读取未因关闭而结束: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("关闭 PTY 未唤醒读取")
			}
		})
	}
}

func TestCloseWakesBlockedTerminalRead(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	// 等待读取协程消费提示符，进入下一次阻塞读取。
	time.Sleep(50 * time.Millisecond)
	if err := f.s.Close(); err != nil {
		t.Fatalf("关闭终端失败: %v", err)
	}
	select {
	case <-f.readEnd:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("关闭 PTY 后读取仍在阻塞")
	}
	select {
	case <-f.s.exited:
	default:
		t.Fatal("终端结束后 Shell 尚未退出")
	}
}

func TestOutputVisibleBeforeCompletion(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	type outcome struct {
		output string
		code   int
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		output, code, err := f.run(t, "printf begin; sleep 1; printf end")
		result <- outcome{output, code, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(f.screen(), "printf end\r\nbegin") {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(f.screen(), "printf end\r\nbegin") {
		t.Fatal("命令结束前 GUI 未显示输出")
	}
	select {
	case <-result:
		t.Fatal("观察首段输出之前命令已结束")
	default:
	}
	r := <-result
	if r.err != nil || r.code != 0 || r.output != "beginend" {
		t.Fatalf("输出不一致: %q %d %v", r.output, r.code, r.err)
	}
}

func TestInterruptCommand(t *testing.T) {
	for _, shellPath := range []string{"/bin/zsh", "/opt/homebrew/bin/fish"} {
		t.Run(filepath.Base(shellPath), func(t *testing.T) {
			f := newFixture(t, shellPath)
			var tester *ui.Tester
			if f.term != nil {
				tester = ui.NewTester(func(c *ui.Context) { terminal.View(c, f.term).Fill().AutoFocus() }, 640, 400)
			}
			type outcome struct {
				code int
				err  error
			}
			result := make(chan outcome, 1)
			go func() {
				_, code, err := f.run(t, "sleep 60")
				result <- outcome{code, err}
			}()
			waitForegroundGroup(t, f.s)
			if f.s.OnKey(ui.Ctrl, ui.KeyC) {
				t.Fatal("CLI 执行期间阻止了 Ctrl+C")
			}
			if tester != nil {
				tester.Key(ui.Ctrl, ui.KeyC)
			} else if _, err := f.s.Write([]byte{3}); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-result:
				if result.err == nil && result.code == 0 {
					t.Fatal("中断的命令被报告为成功")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("中断后 CLI 未返回")
			}
			if output, code, err := f.run(t, "printf resumed"); err != nil || code != 0 || output != "resumed" {
				t.Fatalf("中断后不能继续使用: %q %d %v", output, code, err)
			}
		})
	}
}

func TestFishManualInputIsPreserved(t *testing.T) {
	f := newFixture(t, "/opt/homebrew/bin/fish")
	tester := ui.NewTester(func(c *ui.Context) { terminal.View(c, f.term).Fill().AutoFocus() }, 640, 400)
	tester.Type("echo pending-input")
	if _, _, err := f.run(t, "printf should-not-run"); err == nil {
		t.Fatal("fish 有未提交的人工输入时接受了 CLI 命令")
	}
	tester.Key(0, ui.KeyEnter)
	f.waitReady(t)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !strings.Contains(f.screen(), "\npending-input\n") {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(f.screen(), "\npending-input\n") {
		t.Fatalf("人工输入未执行: %q", f.screen())
	}
	if output, code, err := f.run(t, "printf resumed"); err != nil || code != 0 || output != "resumed" {
		t.Fatalf("人工输入后不能继续执行: %q %d %v", output, code, err)
	}
}

func TestFishDirectoryMarkerSplit(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "capture-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	s := &Session{kind: "fish"}
	r := &request{output: output}
	input := []byte("\x1b[31mred\x1b[0m\x1b]7;file://host/tmp\aA\x1b]7;file://host/other\x1b\\B\x1b]8;;link\aC")
	for _, b := range input {
		s.captureOutput(r, []byte{b}, false)
	}
	s.captureOutput(r, nil, true)
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(output)
	if err != nil || string(got) != "\x1b[31mred\x1b[0mAB\x1b]8;;link\aC" {
		t.Fatalf("OSC 7 过滤输出 = %q，错误 %v", got, err)
	}
}

func TestKittyKeyIsNotTerminalReply(t *testing.T) {
	s := &Session{ready: true}
	key := []byte("\x1b[101u")
	if terminalReply.Match(key) {
		t.Fatal("Kitty 键盘字符被误判为终端回复")
	}
	s.trackInput(key)
	if !s.dirty {
		t.Fatal("Kitty 键盘字符未标记为人工输入")
	}
	s.trackInput([]byte("\x1b[13u"))
	if s.dirty || s.pending != 1 || s.ready {
		t.Fatalf("Kitty 回车状态不正确: %+v", s)
	}
}

func TestKittyControlC(t *testing.T) {
	for _, p := range [][]byte{[]byte{3}, []byte("\x1b[99;5u"), []byte("\x1b[67;6:1u")} {
		if !isInterrupt(p) {
			t.Fatalf("未识别 Ctrl+C: %q", p)
		}
	}
	if isInterrupt([]byte("\x1b[99u")) || isInterrupt([]byte("\x1b[99;3u")) {
		t.Fatal("普通按键被误认作 Ctrl+C")
	}
}

func TestControlSequenceSplitBeforeCommand(t *testing.T) {
	r := &request{}
	input := []byte("hidden\x1b]0;secret\a\x1b]11;?\x1b\\\x1b[6n\x1b[0c\x1b[?2004hsecret")
	var output []byte
	for _, b := range input {
		output = append(output, terminalControl(r, []byte{b})...)
	}
	if string(output) != "\x1b]11;?\x1b\\\x1b[6n\x1b[0c\x1b[?2004h" {
		t.Fatalf("终端查询被丢弃或文本回显泄漏: %q", output)
	}
}

func TestMarkerSplitAcrossReads(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "output-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	s := &Session{initSeen: true, initial: true, marker: []byte("\x1eWT:test:"), active: &request{output: output, done: make(chan struct{})}}
	input := []byte("hidden\x1eWT:test:S\x1ffirst\r\nsecond\x1eWT:test:E:7\x1f\x1eWT:test:P\x1ftail")
	for _, b := range input {
		s.readBuf = append(s.readBuf, b)
		s.parse(false)
	}
	if s.active != nil || !strings.HasSuffix(string(s.parsed), "first\r\nsecondtail") {
		t.Fatalf("拆分标记解析失败: %q", s.parsed)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(output)
	if err != nil || string(data) != "first\r\nsecond" {
		t.Fatalf("捕获输出 = %q，错误 %v", data, err)
	}
}
