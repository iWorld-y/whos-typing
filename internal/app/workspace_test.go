package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"whos-typing/internal/control"
)

type workspaceExecutor struct{ id string }

func (e workspaceExecutor) Execute(context.Context, string) control.Result { return control.Result{} }

func testWorkspace() *workspace {
	return newWorkspace(func(id string, _ func(int)) (*pane, error) { return &pane{target: workspaceExecutor{id: id}}, nil })
}

func TestWorkspaceNestedSplitAndCollapse(t *testing.T) {
	w := testWorkspace()
	a, err := w.NewTab(true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.SplitSession(a.SessionID, "right", true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := w.SplitSession(b.SessionID, "down", true)
	if err != nil {
		t.Fatal(err)
	}
	d, err := w.SplitSession(c.SessionID, "right", true)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	root := w.tabs[0].root
	if root.direction != "right" || root.percent != 50 || root.first.paneID != a.SessionID || root.second.direction != "down" || root.second.first.paneID != b.SessionID || root.second.second.direction != "right" || root.second.second.first.paneID != c.SessionID || root.second.second.second.paneID != d.SessionID {
		t.Fatal("嵌套分屏结构错误")
	}
	root.percent = 65
	w.mu.Unlock()
	w.remove(c.SessionID, nil)
	w.mu.Lock()
	root = w.tabs[0].root
	if root.percent != 65 || root.second.second.paneID != d.SessionID || w.currentLocked() != d.SessionID {
		t.Fatalf("关闭后树或焦点错误: %+v", root)
	}
	w.mu.Unlock()
	if _, _, err := w.ResolveSession(c.SessionID); err == nil {
		t.Fatal("关闭的 ID 不应可寻址")
	}
	e, err := w.SplitSession(d.SessionID, "down", false)
	if err != nil {
		t.Fatal(err)
	}
	if e.SessionID == c.SessionID || e.SessionID == d.SessionID {
		t.Fatal("新会话复用了 ID")
	}
	other := testWorkspace()
	f, err := other.NewTab(true)
	if err != nil {
		t.Fatal(err)
	}
	if f.SessionID == a.SessionID || f.SessionID == e.SessionID {
		t.Fatal("重建工作区后 ID 被复用")
	}
}

func TestWorkspaceFocusTabsAndResolution(t *testing.T) {
	w := testWorkspace()
	a, _ := w.NewTab(true)
	b, _ := w.SplitSession(a.SessionID, "right", false)
	current, _ := w.CurrentSession()
	if current.SessionID != a.SessionID {
		t.Fatal("后台分屏改变焦点")
	}
	c, _ := w.NewTab(false)
	current, _ = w.CurrentSession()
	if current.SessionID != a.SessionID {
		t.Fatal("后台标签页改变焦点")
	}
	if _, _, err := w.ResolveSession(""); err == nil || !strings.Contains(err.Error(), "多个") {
		t.Fatalf("多个会话未拒绝省略目标: %v", err)
	}
	if _, _, err := w.ResolveSession("unknown"); err == nil {
		t.Fatal("未知 ID 未拒绝")
	}
	if err := w.FocusSession(b.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := w.FocusSession(c.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := w.FocusSession(a.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := w.FocusSession(b.SessionID); err != nil {
		t.Fatal(err)
	}
	w.closeTab(c.TabID)
	current, _ = w.CurrentSession()
	if current.SessionID != b.SessionID {
		t.Fatalf("关闭其他标签页改变焦点: %+v", current)
	}
	w.remove(b.SessionID, nil)
	current, _ = w.CurrentSession()
	if current.SessionID != a.SessionID {
		t.Fatalf("关闭窗格后焦点错误: %+v", current)
	}
	id, _, err := w.ResolveSession("")
	if err != nil || id != a.SessionID {
		t.Fatalf("单会话兼容失败: %s %v", id, err)
	}
	if _, _, err := w.ResolveSession(c.SessionID); err == nil {
		t.Fatal("旧 ID 不应回退到唯一会话")
	}
	var empty atomic.Int32
	w.onEmpty = func() { empty.Add(1) }
	w.remove(a.SessionID, nil)
	w.remove(a.SessionID, nil)
	if empty.Load() != 1 {
		t.Fatalf("最后会话关闭次数: %d", empty.Load())
	}
}

func TestWorkspaceCreateFailureAndTargetCloses(t *testing.T) {
	var count atomic.Int32
	started, proceed := make(chan struct{}), make(chan struct{})
	var closed atomic.Int32
	w := newWorkspace(func(id string, _ func(int)) (*pane, error) {
		n := count.Add(1)
		if n == 3 {
			close(started)
			<-proceed
		}
		if n == 4 {
			return &pane{closeFn: func() { closed.Add(1) }}, errors.New("注入失败")
		}
		return &pane{target: workspaceExecutor{id: id}, closeFn: func() { closed.Add(1) }}, nil
	})
	a, err := w.NewTab(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.NewTab(false); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := w.SplitSession(a.SessionID, "right", true); done <- err }()
	<-started
	w.remove(a.SessionID, nil)
	close(proceed)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "关闭") {
		t.Fatalf("目标关闭后应拒绝分屏: %v", err)
	}
	if closed.Load() != 2 {
		t.Fatalf("目标和新资源未回收: %d", closed.Load())
	}
	if _, err := w.NewTab(true); err == nil {
		t.Fatal("注入失败未报告")
	}
	if closed.Load() != 3 {
		t.Fatalf("部分创建的资源未回收: %d", closed.Load())
	}
	list, _ := w.ListSessions()
	if len(list.Sessions) != 1 {
		t.Fatalf("失败留下会话: %+v", list)
	}
}

func TestWorkspaceLateExitAndShutdownDuringCreate(t *testing.T) {
	var callbacks []func(int)
	var mu sync.Mutex
	started, proceed := make(chan struct{}), make(chan struct{})
	var closed atomic.Int32
	w := newWorkspace(func(id string, exit func(int)) (*pane, error) {
		mu.Lock()
		callbacks = append(callbacks, exit)
		n := len(callbacks)
		mu.Unlock()
		if n == 3 {
			close(started)
			<-proceed
		}
		return &pane{target: workspaceExecutor{id: id}, closeFn: func() { closed.Add(1) }}, nil
	})
	a, _ := w.NewTab(true)
	_, _ = w.NewTab(false)
	w.remove(a.SessionID, nil)
	mu.Lock()
	late := callbacks[0]
	mu.Unlock()
	late(0)
	done := make(chan error, 1)
	go func() { _, err := w.NewTab(false); done <- err }()
	<-started
	w.shutdown()
	close(proceed)
	if err := <-done; err == nil {
		t.Fatal("关闭中的创建应失败")
	}
	if closed.Load() != 3 {
		t.Fatalf("资源关闭次数: %d", closed.Load())
	}
}

func TestWorkspaceInitialFailurePreservesReasonAndIgnoresLateExit(t *testing.T) {
	w := testWorkspace()
	created, err := w.NewTab(true)
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	p := w.panes[created.SessionID]
	p.life = &session{err: errors.New("启动 Shell 失败: 可执行文件不存在")}
	w.mu.Unlock()
	var reported error
	var initial bool
	w.onError = func(got *pane, err error) { initial = got.initial; reported = err }
	w.exited(p, -1)
	if !initial || reported == nil || !strings.Contains(reported.Error(), "可执行文件不存在") {
		t.Fatalf("首次失败原因丢失: initial=%v err=%v", initial, reported)
	}
	reported = nil
	w.exited(p, -1)
	if reported != nil {
		t.Fatalf("已移除会话的迟到回调不应报告错误: %v", reported)
	}
}

func TestWorkspaceTabNumberingAndRename(t *testing.T) {
	w := testWorkspace()
	a, _ := w.NewTab(true)
	b, _ := w.NewTab(false)
	list, _ := w.ListSessions()
	if list.Sessions[0].TabTitle != "01" || list.Sessions[1].TabTitle != "02" {
		t.Fatalf("默认编号错误: %+v", list)
	}
	w.closeTab(b.TabID)
	c, err := w.NewTab(true)
	if err != nil {
		t.Fatal(err)
	}
	d, err := w.SplitSession(a.SessionID, "right", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RenameTab(a.TabID, "构建"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []struct{ id, title string }{{a.TabID, ""}, {a.TabID, " \t\n"}, {"missing", "其他"}} {
		if err := w.RenameTab(bad.id, bad.title); err == nil {
			t.Fatalf("无效重命名未拒绝: %+v", bad)
		}
	}
	list, _ = w.ListSessions()
	if len(list.Sessions) != 3 || list.Sessions[0].TabTitle != "构建" || list.Sessions[1].TabTitle != "构建" || list.Sessions[1].SessionID != d.SessionID || list.Sessions[2].TabTitle != "03" {
		t.Fatalf("重命名、分屏或关闭后编号错误: %+v", list)
	}
	current, _ := w.CurrentSession()
	if current.SessionID != c.SessionID || current.TabTitle != "03" {
		t.Fatalf("后台重命名改变焦点: %+v", current)
	}
	other := testWorkspace()
	_, _ = other.NewTab(true)
	first, _ := other.CurrentSession()
	if first.TabTitle != "01" {
		t.Fatalf("新工作区默认编号错误: %+v", first)
	}
}

func TestWorkspaceTabNumberNotConsumedBySplitOrFailedCreate(t *testing.T) {
	var calls atomic.Int32
	w := newWorkspace(func(id string, _ func(int)) (*pane, error) {
		if calls.Add(1) == 3 {
			return nil, errors.New("注入创建失败")
		}
		return &pane{target: workspaceExecutor{id: id}}, nil
	})
	a, _ := w.NewTab(true)
	b, err := w.SplitSession(a.SessionID, "right", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.NewTab(false); err == nil {
		t.Fatal("应报告创建失败")
	}
	c, err := w.NewTab(false)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := w.ListSessions()
	if len(list.Sessions) != 3 || list.Sessions[0].TabTitle != "01" || list.Sessions[1].TabTitle != "01" || list.Sessions[1].SessionID != b.SessionID || list.Sessions[2].SessionID != c.SessionID || list.Sessions[2].TabTitle != "02" {
		t.Fatalf("失败或分屏消耗了编号: %+v", list)
	}
}

func TestWorkspaceTabNumberAboveNinetyNine(t *testing.T) {
	w := testWorkspace()
	for i := 1; i <= 100; i++ {
		item, err := w.NewTab(i == 100)
		if err != nil {
			t.Fatal(err)
		}
		if i == 100 {
			current, err := w.CurrentSession()
			if err != nil || current.TabID != item.TabID || current.TabTitle != "100" {
				t.Fatalf("三位编号错误: %+v %v", current, err)
			}
		}
	}
}
