package app

import (
	"fmt"
	"strings"
	"sync"
)

type terminalSession interface {
	Close() error
	Done() <-chan struct{}
	Text() string
}

type terminalWindow interface {
	Close()
}

// session 协调终端与窗口的退出，保护 Shell 提前退出和重复关闭的情况。
type session struct {
	ready     chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	terminal  terminalSession
	window    terminalWindow
	closed    bool
	err       error
}

func newSession() *session {
	return &session{ready: make(chan struct{})}
}

func (s *session) setReady(term terminalSession, win terminalWindow) {
	s.mu.Lock()
	s.terminal = term
	s.window = win
	s.mu.Unlock()
	close(s.ready)
}

// onExit 可能由 Close 同步调用，因此不能在回调内等待窗口或关闭窗口。
func (s *session) onExit(code int) {
	go func() {
		<-s.ready
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		if code == -1 {
			reason := strings.TrimSpace(s.terminal.Text())
			if reason == "" {
				reason = "终端未提供详细原因"
			}
			s.err = fmt.Errorf("启动 Shell 失败: %s", reason)
		}
		win := s.window
		s.mu.Unlock()
		win.Close()
	}()
}

// close 在结束应用之前等待终端通知会话已结束。
func (s *session) close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		term := s.terminal
		s.mu.Unlock()
		if term != nil {
			_ = term.Close()
			<-term.Done()
		}
	})
}

func (s *session) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *session) error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
