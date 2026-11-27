// 本文件验证受管 SSH 标签页的创建、失败回收及普通新建仍走本地 Shell。
package app

import (
	"errors"
	"strings"
	"testing"
)

func TestWorkspaceOpenManagedSSHCallsManagedFactory(t *testing.T) {
	w := testWorkspace(t)
	var gotTarget, gotConfig string
	w.managedFactory = func(id string, _ func(int), target, config string) (*pane, error) {
		gotTarget, gotConfig = target, config
		return &pane{target: workspaceExecutor{id: id}}, nil
	}
	created, err := w.OpenManagedSSH("wt-fixture", "/tmp/config", true)
	if err != nil {
		t.Fatal(err)
	}
	if gotTarget != "wt-fixture" || gotConfig != "/tmp/config" {
		t.Fatalf("目标或配置未传递: %q %q", gotTarget, gotConfig)
	}
	if created.SessionID == "" || created.TabID == "" {
		t.Fatalf("创建响应: %+v", created)
	}
	list, _ := w.ListSessions()
	if len(list.Sessions) != 1 {
		t.Fatalf("受管会话数量: %+v", list)
	}
	// 普通新建标签仍走本地 factory，不复用受管入口。
	if _, err := w.NewTab(false); err != nil {
		t.Fatal(err)
	}
	list, _ = w.ListSessions()
	if len(list.Sessions) != 2 {
		t.Fatalf("普通新建标签未创建独立会话: %+v", list)
	}
}

func TestWorkspaceOpenManagedSSHFailureLeavesNoLayout(t *testing.T) {
	w := testWorkspace(t)
	w.managedFactory = func(id string, _ func(int), target, config string) (*pane, error) {
		return nil, errors.New("启动受管会话失败: 连接不可用")
	}
	if _, err := w.OpenManagedSSH("wt-fixture", "", true); err == nil || !strings.Contains(err.Error(), "启动受管会话失败") {
		t.Fatalf("启动失败应报告: %v", err)
	}
	list, _ := w.ListSessions()
	if len(list.Sessions) != 0 {
		t.Fatalf("失败留下空布局: %+v", list)
	}
}

func TestWorkspaceOpenManagedSSHUnsupported(t *testing.T) {
	w := testWorkspace(t)
	if _, err := w.OpenManagedSSH("wt-fixture", "", false); err == nil || !strings.Contains(err.Error(), "不支持受管 SSH") {
		t.Fatalf("无受管工厂应拒绝: %v", err)
	}
}
