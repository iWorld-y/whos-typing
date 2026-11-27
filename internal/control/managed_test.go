// 本文件验证受管 SSH 创建入口的能力协商和旧窗口拒绝。
package control

import (
	"errors"
	"strings"
	"testing"
)

type managedWorkspaceStub struct {
	*workspaceStub
	target string
	config string
	err    error
}

func (m *managedWorkspaceStub) OpenManagedSSH(target, config string, _ bool) (CreatedSession, error) {
	if m.err != nil {
		return CreatedSession{}, m.err
	}
	m.target, m.config = target, config
	return CreatedSession{SessionID: "managed-1", TabID: "tab-managed"}, nil
}

func TestOpenManagedSSHRoundTrip(t *testing.T) {
	stub := &managedWorkspaceStub{workspaceStub: &workspaceStub{sessions: map[string]Executor{}, current: ""}}
	path := readTestServer(t, stub)
	created, err := openManagedSSH(path, "wt-fixture", "/tmp/config", false)
	if err != nil {
		t.Fatal(err)
	}
	if created.SessionID != "managed-1" || created.TabID != "tab-managed" {
		t.Fatalf("创建响应: %+v", created)
	}
	if stub.target != "wt-fixture" || stub.config != "/tmp/config" {
		t.Fatalf("目标或配置未传递: %q %q", stub.target, stub.config)
	}
}

func TestOpenManagedSSHOldGUIRejected(t *testing.T) {
	stub := &workspaceStub{sessions: map[string]Executor{}, current: ""}
	path := readTestServer(t, stub)
	_, err := openManagedSSH(path, "wt-fixture", "", false)
	if err == nil || !strings.Contains(err.Error(), "不支持受管 SSH") {
		t.Fatalf("旧窗口应拒绝新入口: %v", err)
	}
}

func TestOpenManagedSSHFailurePropagates(t *testing.T) {
	stub := &managedWorkspaceStub{
		workspaceStub: &workspaceStub{sessions: map[string]Executor{}, current: ""},
		err:           errors.New("连接不可用，请显式 connect；受管会话未启动"),
	}
	path := readTestServer(t, stub)
	_, err := openManagedSSH(path, "wt-fixture", "", false)
	if err == nil || !strings.Contains(err.Error(), "连接不可用") {
		t.Fatalf("启动失败应原样报告: %v", err)
	}
}
