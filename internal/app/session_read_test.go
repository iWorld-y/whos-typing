package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"whos-typing/internal/shell"
)

type passiveConn struct {
	closed chan struct{}
	once   sync.Once
	writes atomic.Int32
}

type blockingTerminal struct {
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
	done    chan struct{}
}

func (t *blockingTerminal) Text() string {
	close(t.started)
	<-t.release
	return "snapshot"
}

func (t *blockingTerminal) Close() error {
	close(t.closed)
	close(t.done)
	return nil
}

func (t *blockingTerminal) Done() <-chan struct{} { return t.done }

func (c *passiveConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *passiveConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return len(p), nil
}

func (c *passiveConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func requireTerminal(t *testing.T) {
	t.Helper()
	if os.Getenv("MYGO_GHOSTTY_VT") == "" {
		t.Skip("需要 MYGO_GHOSTTY_VT 指向上游终端库")
	}
}

func TestSessionReadLifecycle(t *testing.T) {
	s := newSession()
	if _, err := s.ReadSession(); err == nil || !strings.Contains(err.Error(), "未启动") {
		t.Fatalf("终端尚未启动: %v", err)
	}
	term := &testTerminal{text: "历史\n屏幕😀", done: make(chan struct{}), closed: make(chan struct{})}
	s.setReady(term, &testWindow{closed: make(chan struct{})})
	if got, err := s.ReadSession(); err != nil || got != term.text {
		t.Fatalf("终端文本错误: %q %v", got, err)
	}
	close(term.done)
	s.close()
	if _, err := s.ReadSession(); err == nil || !strings.Contains(err.Error(), "已结束") {
		t.Fatalf("终端结束后读取: %v", err)
	}
}

func TestSessionReadCompletesBeforeClose(t *testing.T) {
	s := newSession()
	term := &blockingTerminal{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), done: make(chan struct{})}
	s.setReady(term, &testWindow{closed: make(chan struct{})})
	read := make(chan error, 1)
	go func() {
		text, err := s.ReadSession()
		if text != "snapshot" || err != nil {
			read <- fmt.Errorf("读取结果错误: %q %v", text, err)
			return
		}
		read <- nil
	}()
	waitFor(t, term.started)
	closed := make(chan struct{})
	go func() { s.close(); close(closed) }()
	select {
	case <-term.closed:
		t.Fatal("读取未完成时关闭了终端")
	case <-time.After(20 * time.Millisecond):
	}
	close(term.release)
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("读取未结束")
	}
	waitFor(t, closed)
	if _, err := s.ReadSession(); err == nil || !strings.Contains(err.Error(), "已结束") {
		t.Fatalf("关闭后读取未报告错误: %v", err)
	}
}

func TestSessionReadTerminalScrollbackWithoutInput(t *testing.T) {
	requireTerminal(t)
	conn := &passiveConn{closed: make(chan struct{})}
	term, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })
	term.Resize(40, 5)
	var text strings.Builder
	for i := range 30 {
		fmt.Fprintf(&text, "历史%02d😀\r\n", i)
	}
	term.Feed([]byte(text.String()))
	s := newSession()
	s.setReady(term, &testWindow{closed: make(chan struct{})})
	for range 5 {
		got, err := s.ReadSession()
		if err != nil || !strings.Contains(got, "历史00😀") || !strings.Contains(got, "历史29😀") {
			t.Fatalf("滚动历史或当前屏幕缺失: %q %v", got, err)
		}
	}
	if conn.writes.Load() != 0 {
		t.Fatal("读取终端文本向会话发送了输入")
	}
}

func TestSessionReadResizeAndAlternateScreen(t *testing.T) {
	requireTerminal(t)
	conn := &passiveConn{closed: make(chan struct{})}
	term, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })
	term.Resize(10, 3)
	long := "BEGIN_LONG_" + strings.Repeat("x", 134) + "_END_LONG"
	term.Feed([]byte(long))
	s := newSession()
	s.setReady(term, &testWindow{closed: make(chan struct{})})
	for _, size := range [][2]int{{10, 3}, {35, 7}, {8, 2}} {
		term.Resize(size[0], size[1])
		text, err := s.ReadSession()
		if err != nil || !strings.Contains(text, long) {
			t.Fatalf("尺寸 %v 下长行不完整: %q %v", size, text, err)
		}
	}
	term.Resize(14, 3)
	term.Feed([]byte("\r\nMAIN_HISTORY\r\n\x1b[?1049h\x1b[2J\x1b[H"))
	for i := range 20 {
		term.Feed([]byte(fmt.Sprintf("ALT_%02d\r\n", i)))
	}
	for _, size := range [][2]int{{14, 3}, {14, 8}} {
		term.Resize(size[0], size[1])
		text, err := s.ReadSession()
		if err != nil || strings.Contains(text, "MAIN_HISTORY") || strings.Contains(text, "ALT_00") || !strings.Contains(text, "ALT_19") {
			t.Fatalf("替代屏幕的内容不符合终端缓冲区: %q %v", text, err)
		}
	}
	term.Feed([]byte("\x1b[?1049l"))
	text, err := s.ReadSession()
	if err != nil || !strings.Contains(text, "MAIN_HISTORY") {
		t.Fatalf("退出替代屏幕后普通屏幕未恢复: %q %v", text, err)
	}
	if conn.writes.Load() != 0 {
		t.Fatal("读取或调整隔离终端时向会话发送了输入")
	}
}

func TestSessionReadAutoWrapAndClippedOutput(t *testing.T) {
	requireTerminal(t)
	for _, tc := range []struct {
		name, input, want string
	}{
		{"auto_wrap", "0123456789ABCDEFGHIJ", "0123456789ABCDEFGHIJ"},
		{"no_wrap", "\x1b[?7l0123456789ABCDEFGHIJ", "012345678J"},
		{"program_clipped", "0123456789", "0123456789"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &passiveConn{closed: make(chan struct{})}
			term, err := terminal.New(terminal.Options{Conn: conn})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = term.Close() })
			term.Resize(10, 3)
			term.Feed([]byte(tc.input))
			s := newSession()
			s.setReady(term, &testWindow{closed: make(chan struct{})})
			got, err := s.ReadSession()
			if err != nil || got != tc.want {
				t.Fatalf("终端文本 = %q，期望 %q，错误 %v", got, tc.want, err)
			}
		})
	}
}

func TestSessionReadWhileShellRuns(t *testing.T) {
	requireTerminal(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("SHELL", "/bin/zsh")
	sh, err := shell.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	term, err := terminal.New(terminal.Options{Conn: sh})
	if err != nil {
		_ = sh.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })
	sh.SetSender(term.Send)
	s := newSession()
	s.setReady(term, &testWindow{closed: make(chan struct{})})
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result := sh.Execute(ctx, "true")
		cancel()
		if result.Output != nil {
			_ = os.Remove(result.Output.Name())
			_ = result.Output.Close()
		}
		if result.Err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("隔离 Shell 未就绪: %v", result.Err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	result := make(chan error, 1)
	go func() {
		got := sh.Execute(context.Background(), `printf '%s%s' WT_ RUNNING; sleep 2; printf '%s%s' WT_ DONE`)
		if got.Output == nil {
			result <- fmt.Errorf("原命令无输出文件: %v", got.Err)
			return
		}
		defer os.Remove(got.Output.Name())
		defer got.Output.Close()
		output, err := io.ReadAll(got.Output)
		if err != nil || got.Err != nil || got.ExitCode != 0 || string(output) != "WT_RUNNINGWT_DONE" {
			result <- fmt.Errorf("原命令结果错误: %q %d %v %v", output, got.ExitCode, got.Err, err)
			return
		}
		result <- nil
	}()
	deadline = time.Now().Add(time.Second)
	for {
		text, err := s.ReadSession()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(text, "WT_RUNNING") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("命令执行中的输出未出现在快照: %q", text)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-result:
		t.Fatalf("读取时命令不应已完成: %v", err)
	default:
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("隔离 Shell 命令未完成")
	}
}
