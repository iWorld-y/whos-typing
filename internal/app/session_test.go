package app

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testTerminal struct {
	text       string
	done       chan struct{}
	closed     chan struct{}
	closeCount atomic.Int32
	onClose    func()
}

func (t *testTerminal) Close() error {
	if t.closeCount.Add(1) == 1 {
		close(t.closed)
		if t.onClose != nil {
			t.onClose()
		}
	}
	return nil
}

func (t *testTerminal) Done() <-chan struct{} { return t.done }
func (t *testTerminal) Text() string          { return t.text }

type testWindow struct {
	closed chan struct{}
	once   sync.Once
}

func (w *testWindow) Close() { w.once.Do(func() { close(w.closed) }) }

func waitFor(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("等待操作完成超时")
	}
}

func TestExitBeforeWindowReady(t *testing.T) {
	s := newSession()
	term := &testTerminal{done: make(chan struct{}), closed: make(chan struct{})}
	win := &testWindow{closed: make(chan struct{})}

	s.onExit(0)
	s.setReady(term, win)
	waitFor(t, win.closed)
	if err := s.error(); err != nil {
		t.Fatalf("正常退出不应返回错误: %v", err)
	}
}

func TestShellExitAndStartFailure(t *testing.T) {
	for _, tt := range []struct {
		name    string
		code    int
		text    string
		wantErr string
	}{
		{name: "shell exits nonzero", code: 42},
		{name: "shell fails to start", code: -1, text: "fork/exec /missing: no such file", wantErr: "fork/exec /missing: no such file"},
		{name: "failure without detail", code: -1, wantErr: "终端未提供详细原因"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newSession()
			term := &testTerminal{text: tt.text, done: make(chan struct{}), closed: make(chan struct{})}
			win := &testWindow{closed: make(chan struct{})}
			s.setReady(term, win)
			s.onExit(tt.code)
			waitFor(t, win.closed)
			err := s.error()
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Shell 退出不应返回启动错误: %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("启动错误 = %v，期望包含 %q", err, tt.wantErr)
			}
		})
	}
}

func TestCloseWaitsForTerminalAndIsIdempotent(t *testing.T) {
	s := newSession()
	term := &testTerminal{done: make(chan struct{}), closed: make(chan struct{})}
	term.onClose = func() { s.onExit(-1) }
	win := &testWindow{closed: make(chan struct{})}
	s.setReady(term, win)

	finished := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(s.close)
		}
		wg.Wait()
		close(finished)
	}()
	waitFor(t, term.closed)
	select {
	case <-finished:
		t.Fatal("终端通知会话结束前，关闭操作已返回")
	default:
	}
	close(term.done)
	waitFor(t, finished)
	if got := term.closeCount.Load(); got != 1 {
		t.Fatalf("终端关闭次数 = %d，期望 1", got)
	}
	if err := s.error(); err != nil {
		t.Fatalf("主动关闭不应报告启动错误: %v", err)
	}
}
