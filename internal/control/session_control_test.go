package control

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type controlExecutor struct {
	mu    sync.Mutex
	owner string
}

func (e *controlExecutor) Execute(context.Context, string) Result {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.owner == "human_takeover" {
		return Result{Err: errors.New("终端已由人类接管")}
	}
	e.owner = "agent"
	return Result{}
}

func (e *controlExecutor) ControlStatus() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owner
}

func (e *controlExecutor) ReleaseControl() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.owner == "human_takeover" {
		return errors.New("终端已由人类接管")
	}
	e.owner = "human"
	return nil
}

func (e *controlExecutor) ReadSession() (string, error) { return "visible", nil }

func TestSessionControlProtocol(t *testing.T) {
	executor := &controlExecutor{owner: "human"}
	path := readTestServer(t, executor)
	var status bytes.Buffer
	if err := sessionStatus(path, &status); err != nil || status.String() != "human\n" {
		t.Fatalf("初始状态错误: %q %v", status.String(), err)
	}
	if code, err := run(path, "printf user-cli", &bytes.Buffer{}); err != nil || code != 0 {
		t.Fatalf("现有 CLI 请求失败: %d %v", code, err)
	}
	status.Reset()
	if err := sessionStatus(path, &status); err != nil || status.String() != "agent\n" {
		t.Fatalf("CLI 执行后应为 Agent 控制: %q %v", status.String(), err)
	}
	if err := sessionRelease(path); err != nil || executor.ControlStatus() != "human" {
		t.Fatalf("主动交还失败: %v", err)
	}
	executor.mu.Lock()
	executor.owner = "human_takeover"
	executor.mu.Unlock()
	if _, err := run(path, "printf blocked", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "人类") {
		t.Fatalf("人类接管期间未拒绝命令: %v", err)
	}
	if err := sessionRelease(path); err == nil || executor.ControlStatus() != "human_takeover" {
		t.Fatalf("CLI 不应解除人类接管: %v", err)
	}
	var screen bytes.Buffer
	if err := readSession(path, &screen); err != nil || screen.String() != "visible" {
		t.Fatalf("人类接管期间读取失败: %q %v", screen.String(), err)
	}
	if got := executor.ControlStatus(); got != "human_takeover" {
		t.Fatalf("读取快照不能改变控制权: %q", got)
	}
}
