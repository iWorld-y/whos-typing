package app

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"whos-typing/internal/control"
	"whos-typing/internal/shell"
)

func TestWorkspaceViewSplitFocusAndResize(t *testing.T) {
	requireTerminal(t)
	w := newWorkspace(func(id string, onExit func(int)) (*pane, error) {
		conn := &passiveConn{closed: make(chan struct{})}
		term, err := terminal.New(terminal.Options{Conn: conn, OnExit: onExit})
		if err != nil {
			return nil, err
		}
		view := &controlView{}
		view.control = &fakeViewControl{view: view}
		return &pane{target: workspaceExecutor{id: id}, terminal: term, view: view, closeFn: func() { _ = term.Close() }}, nil
	})
	t.Cleanup(w.shutdown)
	a, _ := w.NewTab(true)
	b, _ := w.SplitSession(a.SessionID, "right", true)
	c, _ := w.SplitSession(b.SessionID, "down", true)
	d, _ := w.SplitSession(c.SessionID, "right", true)
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 1100, 700)
	tester.SetFocused(true)
	tester.Frame()
	box := func(id string) ui.Rect {
		r, ok := tester.Find(fmt.Sprintf("终端 %s", id))
		if !ok {
			t.Fatalf("找不到窗格 %s", id)
		}
		return r
	}
	ra, rb, rc, rd := box(a.SessionID), box(b.SessionID), box(c.SessionID), box(d.SessionID)
	if !(ra.X < rb.X && rb.Y < rc.Y && rc.X < rd.X && rc.Y == rd.Y) {
		t.Fatalf("四窗格位置错误: A=%+v B=%+v C=%+v D=%+v", ra, rb, rc, rd)
	}
	w.mu.Lock()
	before := w.tabs[0].root.percent
	aTerm := w.panes[a.SessionID].terminal
	w.mu.Unlock()
	colsBefore, _ := aTerm.Size()
	x, y := ra.X+ra.W+0.5, ra.Y+ra.H/2
	tester.Press(x, y)
	tester.Move(x+80, y)
	tester.Release(x+80, y)
	tester.Frame()
	w.mu.Lock()
	after := w.tabs[0].root.percent
	w.mu.Unlock()
	colsAfter, _ := aTerm.Size()
	if after <= before || colsAfter <= colsBefore {
		t.Fatalf("拖动未调整比例和终端尺寸: %f->%f %d->%d", before, after, colsBefore, colsAfter)
	}
	rb = box(b.SessionID)
	tester.ClickAt(rb.X+rb.W/2, rb.Y+rb.H/2)
	tester.Frame()
	current, err := w.CurrentSession()
	if err != nil || current.SessionID != b.SessionID {
		t.Fatalf("点击未选择 B: %+v %v", current, err)
	}
	e, err := w.NewTab(false)
	if err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	current, _ = w.CurrentSession()
	if current.SessionID != b.SessionID {
		t.Fatalf("后台新建改变焦点: %+v", current)
	}
	w.mu.Lock()
	bTerm := w.panes[b.SessionID].terminal
	w.mu.Unlock()
	colsBeforeHide, rowsBeforeHide := bTerm.Size()
	if err := tester.Click("02"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	colsHidden, rowsHidden := bTerm.Size()
	if colsHidden != colsBeforeHide || rowsHidden != rowsBeforeHide {
		t.Fatalf("隐藏标签页改变了终端尺寸: %dx%d -> %dx%d", colsBeforeHide, rowsBeforeHide, colsHidden, rowsHidden)
	}
	current, _ = w.CurrentSession()
	if current.SessionID != e.SessionID {
		t.Fatalf("切换标签失败: %+v", current)
	}
	if err := tester.Click("01"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	current, _ = w.CurrentSession()
	if current.SessionID != b.SessionID {
		t.Fatalf("未恢复标签焦点: %+v", current)
	}
	if err := tester.RightClick("01"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("关闭当前窗格"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := w.ResolveSession(b.SessionID); err != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	tester.Frame()
	current, err = w.CurrentSession()
	if err != nil || current.SessionID != c.SessionID || !tester.Focused("终端 "+c.SessionID) {
		t.Fatalf("关闭窗格后焦点错误: %+v %v", current, err)
	}
	if err := tester.RightClick("01"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("关闭标签页"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := w.ResolveSession(a.SessionID); err != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	tester.Frame()
	current, err = w.CurrentSession()
	if err != nil || current.SessionID != e.SessionID || !tester.Focused("终端 "+e.SessionID) {
		t.Fatalf("关闭标签后焦点错误: %+v %v", current, err)
	}
}

func TestWorkspaceManyLongTabTitlesRemainAccessible(t *testing.T) {
	requireTerminal(t)
	w := newWorkspace(func(id string, onExit func(int)) (*pane, error) {
		conn := &passiveConn{closed: make(chan struct{})}
		term, err := terminal.New(terminal.Options{Conn: conn, OnExit: onExit})
		if err != nil {
			return nil, err
		}
		view := &controlView{}
		view.control = &fakeViewControl{view: view}
		return &pane{target: workspaceExecutor{id: id}, terminal: term, view: view, closeFn: func() { _ = term.Close() }}, nil
	})
	t.Cleanup(w.shutdown)
	var created []control.CreatedSession
	for i := 0; i < 7; i++ {
		item, err := w.NewTab(i == 0)
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, item)
		if err := w.RenameTab(item.TabID, fmt.Sprintf("标签%d%s", i, strings.Repeat("很长", 15))); err != nil {
			t.Fatal(err)
		}
	}
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 760, 480)
	tester.SetFocused(true)
	tester.Frame()
	if view.tabsScroll.MaxX <= 0 {
		t.Fatalf("长标题未进入滚动区域: %+v", view.tabsScroll)
	}
	for _, name := range []string{"向左滚动标签", "向右滚动标签"} {
		r, ok := tester.Find(name)
		if !ok || r.X < 0 || r.X+r.W > 760 {
			t.Fatalf("操作入口不可见: %s %+v", name, r)
		}
	}
	for range 10 {
		if err := tester.Click("向右滚动标签"); err != nil {
			t.Fatal(err)
		}
	}
	if view.tabsScroll.X <= 0 {
		t.Fatal("标签无法向右滚动")
	}
	label := string([]rune("标签6" + strings.Repeat("很长", 15))[:20]) + "…"
	if err := tester.Click(label); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	current, err := w.CurrentSession()
	if err != nil || current.SessionID != created[6].SessionID {
		t.Fatalf("长标签无法切换: %+v %v", current, err)
	}
}

type inputCapture struct {
	passiveConn
	mu   sync.Mutex
	text strings.Builder
}

func (c *inputCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text.Write(p)
}
func (c *inputCapture) contents() string { c.mu.Lock(); defer c.mu.Unlock(); return c.text.String() }

func TestWorkspaceKeyboardAndPasteStayInFocusedPane(t *testing.T) {
	requireTerminal(t)
	connections := make(map[string]*inputCapture)
	w := newWorkspace(func(id string, onExit func(int)) (*pane, error) {
		conn := &inputCapture{passiveConn: passiveConn{closed: make(chan struct{})}}
		connections[id] = conn
		term, err := terminal.New(terminal.Options{Conn: conn, OnExit: onExit})
		if err != nil {
			return nil, err
		}
		view := &controlView{}
		view.control = &fakeViewControl{view: view}
		return &pane{target: workspaceExecutor{id: id}, terminal: term, view: view, closeFn: func() { _ = term.Close() }}, nil
	})
	t.Cleanup(w.shutdown)
	a, _ := w.NewTab(true)
	b, _ := w.SplitSession(a.SessionID, "right", true)
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 760, 480)
	tester.SetFocused(true)
	tester.Frame()
	tester.Type("draft-b")
	tester.SetClipboard("-paste-b")
	tester.Key(ui.Cmd, ui.KeyV)
	if err := w.FocusSession(a.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	tester.Type("input-a")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(connections[a.SessionID].contents(), "input-a") && strings.Contains(connections[b.SessionID].contents(), "draft-b-paste-b") {
			break
		}
		time.Sleep(time.Millisecond)
	}
	gotA, gotB := connections[a.SessionID].contents(), connections[b.SessionID].contents()
	if gotA != "input-a" || gotB != "draft-b-paste-b" {
		t.Fatalf("键盘或粘贴串到其他窗格: A=%q B=%q", gotA, gotB)
	}
}

func TestWorkspaceInputMethodCompositionFollowsFocus(t *testing.T) {
	requireTerminal(t)
	connections := make(map[string]*inputCapture)
	w := newWorkspace(func(id string, onExit func(int)) (*pane, error) {
		conn := &inputCapture{passiveConn: passiveConn{closed: make(chan struct{})}}
		connections[id] = conn
		term, err := terminal.New(terminal.Options{Conn: conn, OnExit: onExit})
		if err != nil {
			return nil, err
		}
		view := &controlView{}
		view.control = &fakeViewControl{view: view}
		return &pane{target: workspaceExecutor{id: id}, terminal: term, view: view, closeFn: func() { _ = term.Close() }}, nil
	})
	t.Cleanup(w.shutdown)
	a, _ := w.NewTab(true)
	b, _ := w.NewTab(true)
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 760, 480)
	tester.SetFocused(true)
	tester.Frame()
	if _, ok := tester.TextCaret(); !ok {
		t.Fatal("终端未向输入法提供插入位置")
	}
	tester.Compose("ni", 2)
	if got := connections[b.SessionID].contents(); got != "" {
		t.Fatalf("组合输入不应提前发送: %q", got)
	}
	if err := w.FocusSession(a.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	tester.Compose("你", 1)
	tester.Type("你")
	if err := w.FocusSession(b.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	tester.Compose("好", 1)
	tester.Type("好")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if connections[a.SessionID].contents() == "你" && connections[b.SessionID].contents() == "好" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	gotA, gotB := connections[a.SessionID].contents(), connections[b.SessionID].contents()
	if gotA != "你" || gotB != "好" {
		t.Fatalf("输入法提交串到其他会话: A=%q B=%q", gotA, gotB)
	}
}

func TestRenamedTabIgnoresShellTitle(t *testing.T) {
	requireTerminal(t)
	w := newWorkspace(func(id string, onExit func(int)) (*pane, error) {
		conn := &passiveConn{closed: make(chan struct{})}
		term, err := terminal.New(terminal.Options{Conn: conn, OnExit: onExit})
		if err != nil {
			return nil, err
		}
		view := &controlView{}
		view.control = &fakeViewControl{view: view}
		return &pane{target: workspaceExecutor{id: id}, terminal: term, view: view, closeFn: func() { _ = term.Close() }}, nil
	})
	t.Cleanup(w.shutdown)
	a, _ := w.NewTab(true)
	if err := w.RenameTab(a.TabID, "构建"); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	p := w.panes[a.SessionID]
	w.mu.Unlock()
	p.terminal.Feed([]byte("\x1b]2;来自 Shell 的标题\x07"))
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 760, 480)
	tester.Frame()
	current, err := w.CurrentSession()
	if err != nil || current.TabTitle != "构建" || current.Title != "来自 Shell 的标题" || !tester.HasText("构建") || tester.HasText("来自 Shell 的标题") {
		t.Fatalf("Shell 标题覆盖了标签名称: %+v %v", current, err)
	}
}

func TestWorkspaceShortcutsCreateWithoutSendingInput(t *testing.T) {
	requireTerminal(t)
	var mu sync.Mutex
	var shortcutKeys atomic.Int32
	connections := make(map[string]*inputCapture)
	w := newWorkspace(func(id string, onExit func(int)) (*pane, error) {
		conn := &inputCapture{passiveConn: passiveConn{closed: make(chan struct{})}}
		mu.Lock()
		connections[id] = conn
		mu.Unlock()
		term, err := terminal.New(terminal.Options{Conn: conn, OnExit: onExit, OnKey: func(mods ui.Modifiers, key ui.Key) bool {
			if mods&ui.Cmd != 0 && (key == ui.KeyT || key == ui.KeyD) {
				shortcutKeys.Add(1)
				return true
			}
			return false
		}})
		if err != nil {
			return nil, err
		}
		view := &controlView{owner: shell.ControlAgent}
		view.control = &fakeViewControl{view: view}
		return &pane{target: workspaceExecutor{id: id}, terminal: term, view: view, closeFn: func() { _ = term.Close() }}, nil
	})
	t.Cleanup(w.shutdown)
	first, _ := w.NewTab(true)
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 760, 480)
	tester.SetFocused(true)
	tester.Frame()
	mu.Lock()
	firstConn := connections[first.SessionID]
	mu.Unlock()
	tester.Type("draft")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && firstConn.contents() != "draft" {
		time.Sleep(time.Millisecond)
	}
	if firstConn.contents() != "draft" {
		t.Fatal("测试输入未到达初始会话")
	}
	waitCount := func(n int) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			list, _ := w.ListSessions()
			if len(list.Sessions) == n {
				tester.Frame()
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("等待 %d 个会话超时", n)
	}
	tester.Key(ui.Cmd, ui.KeyT)
	waitCount(2)
	current, _ := w.CurrentSession()
	if current.TabTitle != "02" {
		t.Fatalf("Cmd+T 未聚焦新标签: %+v", current)
	}
	tester.Key(ui.Cmd, ui.KeyD)
	waitCount(3)
	w.mu.Lock()
	tab := w.tabLocked(w.selected)
	right := tab.root.direction
	w.mu.Unlock()
	if right != "right" {
		t.Fatalf("Cmd+D 分屏方向错误: %s", right)
	}
	tester.Key(ui.Cmd|ui.Shift, ui.KeyD)
	waitCount(4)
	time.Sleep(100 * time.Millisecond)
	list, _ := w.ListSessions()
	if len(list.Sessions) != 4 {
		t.Fatalf("Shift+D 创建了多余的会话: %+v", list)
	}
	w.mu.Lock()
	tab = w.tabLocked(w.selected)
	down := tab.root.second.direction
	w.mu.Unlock()
	if down != "down" {
		t.Fatalf("Cmd+Shift+D 分屏方向错误: %s", down)
	}
	mu.Lock()
	defer mu.Unlock()
	for id, conn := range connections {
		want := ""
		if id == first.SessionID {
			want = "draft"
		}
		if got := conn.contents(); got != want {
			t.Fatalf("快捷键改变了会话 %s 输入: %q", id, got)
		}
	}
	w.mu.Lock()
	originalOwner := w.panes[first.SessionID].view.owner
	w.mu.Unlock()
	if originalOwner != shell.ControlAgent {
		t.Fatal("快捷键改变了原会话控制权")
	}
	if shortcutKeys.Load() != 0 {
		t.Fatalf("快捷键到达 Shell.OnKey: %d", shortcutKeys.Load())
	}
	if connections[first.SessionID] == nil {
		t.Fatal("初始会话丢失")
	}
}

func TestRightClickClosesClickedBackgroundTab(t *testing.T) {
	requireTerminal(t)
	w := newWorkspace(func(id string, onExit func(int)) (*pane, error) {
		conn := &passiveConn{closed: make(chan struct{})}
		term, err := terminal.New(terminal.Options{Conn: conn, OnExit: onExit})
		if err != nil {
			return nil, err
		}
		view := &controlView{}
		view.control = &fakeViewControl{view: view}
		return &pane{target: workspaceExecutor{id: id}, terminal: term, view: view, closeFn: func() { _ = term.Close() }}, nil
	})
	t.Cleanup(w.shutdown)
	a, _ := w.NewTab(true)
	b, _ := w.NewTab(false)
	c, _ := w.SplitSession(b.SessionID, "right", false)
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 760, 480)
	tester.SetFocused(true)
	tester.Frame()
	if err := tester.RightClick("02"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("关闭当前窗格"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := w.ResolveSession(b.SessionID); err != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, _, err := w.ResolveSession(b.SessionID); err == nil {
		t.Fatal("未关闭被点标签的最近窗格")
	}
	if _, _, err := w.ResolveSession(c.SessionID); err != nil {
		t.Fatal("错误关闭了兄弟窗格")
	}
	current, _ := w.CurrentSession()
	if current.SessionID != a.SessionID {
		t.Fatalf("右键后台标签改变了当前会话: %+v", current)
	}
	tester.Frame()
	if err := tester.RightClick("02"); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem("关闭标签页"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := w.ResolveSession(c.SessionID); err != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	current, err := w.CurrentSession()
	if err != nil || current.SessionID != a.SessionID {
		t.Fatalf("关闭后台标签影响当前会话: %+v %v", current, err)
	}
}

func TestWorkspaceConcurrentSplitAndDividerDrag(t *testing.T) {
	requireTerminal(t)
	w := newWorkspace(func(id string, onExit func(int)) (*pane, error) {
		conn := &passiveConn{closed: make(chan struct{})}
		term, err := terminal.New(terminal.Options{Conn: conn, OnExit: onExit})
		if err != nil {
			return nil, err
		}
		view := &controlView{}
		view.control = &fakeViewControl{view: view}
		return &pane{target: workspaceExecutor{id: id}, terminal: term, view: view, closeFn: func() { _ = term.Close() }}, nil
	})
	t.Cleanup(w.shutdown)
	a, _ := w.NewTab(true)
	_, _ = w.SplitSession(a.SessionID, "right", false)
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 760, 480)
	tester.SetFocused(true)
	tester.Frame()
	done := make(chan error, 1)
	go func() {
		for range 20 {
			item, err := w.SplitSession(a.SessionID, "down", false)
			if err != nil {
				done <- err
				return
			}
			w.remove(item.SessionID, nil)
		}
		done <- nil
	}()
	for i := 0; i < 20; i++ {
		x := float32(380 + i%2*10)
		tester.Press(x, 200)
		tester.Move(x+5, 200)
		tester.Release(x+5, 200)
		tester.Frame()
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
