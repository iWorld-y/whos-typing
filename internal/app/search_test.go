package app

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"whos-typing/internal/shell"
)

func searchWorkspace(t *testing.T) (*workspace, map[string]*inputCapture) {
	return searchWorkspaceLimit(t, 0)
}

func searchWorkspaceLimit(t *testing.T, scrollback int) (*workspace, map[string]*inputCapture) {
	t.Helper()
	requireTerminal(t)
	connections := map[string]*inputCapture{}
	w := newWorkspace(testIDs(t), func(id string, onExit func(int)) (*pane, error) {
		conn := &inputCapture{passiveConn: passiveConn{closed: make(chan struct{})}}
		connections[id] = conn
		p := &pane{target: workspaceExecutor{id: id}, view: &controlView{owner: shell.ControlAgent}}
		p.view.control = &fakeViewControl{view: p.view}
		term, err := terminal.New(terminal.Options{Conn: conn, OnExit: onExit, OnSearch: p.searchStatus, Scrollback: scrollback})
		if err != nil {
			return nil, err
		}
		p.terminal, p.closeFn = term, func() { _ = term.Close() }
		return p, nil
	})
	t.Cleanup(w.shutdown)
	return w, connections
}

func waitSearch(t *testing.T, tester *ui.Tester, p *pane, total, selected int) {
	t.Helper()
	for range 100 {
		tester.Frame()
		s := p.terminal.SearchStatus()
		if !s.Searching && s.Total == total && s.Selected == selected {
			return
		}
	}
	t.Fatalf("搜索状态错误: %+v", p.terminal.SearchStatus())
}

func TestSearchUIFocusNavigationAndIsolation(t *testing.T) {
	w, connections := searchWorkspace(t)
	a, _ := w.NewTab(true)
	b, _ := w.SplitSession(a.SessionID, "right", false)
	p, other := w.panes[a.SessionID], w.panes[b.SessionID]
	p.terminal.Feed([]byte("needle older\r\n" + strings.Repeat("line\r\n", 30) + "needle newest\r\n"))
	other.terminal.Feed([]byte("needle elsewhere\r\n"))
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 760, 480)
	tester.SetFocused(true)
	tester.Frame()
	tester.Type("draft")
	tester.Key(ui.Cmd, ui.KeyF)
	tester.Frame()
	if !tester.Focused("搜索当前窗格") {
		t.Fatal("Cmd+F 未聚焦搜索框")
	}
	tester.Type("needle")
	waitSearch(t, tester, p, 2, 1)
	if !tester.HasText("1 / 2") || other.terminal.SearchStatus().Text != "" {
		t.Fatalf("计数或窗格隔离错误: %+v", other.terminal.SearchStatus())
	}
	if _, ok := tester.Find("接管"); !ok {
		t.Fatal("搜索栏遮挡控制胶囊")
	}
	tester.Key(0, ui.KeyEnter)
	waitSearch(t, tester, p, 2, 2)
	tester.Key(ui.Shift, ui.KeyEnter)
	waitSearch(t, tester, p, 2, 1)
	tester.Key(ui.Cmd, ui.KeyG)
	waitSearch(t, tester, p, 2, 2)
	tester.Key(ui.Cmd|ui.Shift, ui.KeyG)
	waitSearch(t, tester, p, 2, 1)
	tester.Key(ui.Cmd, ui.KeyG)
	waitSearch(t, tester, p, 2, 2)
	if err := tester.Click(fmt.Sprintf("终端 %s", a.SessionID)); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	tester.Key(ui.Cmd, ui.KeyF)
	tester.Frame()
	if !tester.Focused("搜索当前窗格") || !tester.HasText("2 / 2") || p.terminal.SearchStatus().Selected != 2 {
		t.Fatalf("重复 Cmd+F 改变了搜索状态: %+v", p.search)
	}
	if err := w.FocusSession(b.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if p.search.open || p.terminal.SearchStatus().Text != "" || p.search.query != "needle" {
		t.Fatalf("切换时未关闭并保留搜索词: %+v", p.search)
	}
	if err := w.FocusSession(a.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	tester.Key(ui.Cmd, ui.KeyF)
	tester.Frame()
	waitSearch(t, tester, p, 2, 1)
	if p.search.query != "needle" {
		t.Fatalf("未恢复搜索词: %q", p.search.query)
	}
	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	if p.search.open || p.terminal.SearchStatus().Text != "" || !tester.Focused(fmt.Sprintf("终端 %s", a.SessionID)) {
		t.Fatal("Esc 未关闭搜索并恢复焦点")
	}
	if got := connections[a.SessionID].contents(); got != "draft" {
		t.Fatalf("搜索输入进入 Shell: %q", got)
	}
	if got := connections[b.SessionID].contents(); got != "" {
		t.Fatalf("搜索输入进入其他 Shell: %q", got)
	}
}

func TestSearchEmptyAndMissingResults(t *testing.T) {
	w, _ := searchWorkspace(t)
	a, _ := w.NewTab(true)
	p := w.panes[a.SessionID]
	p.terminal.Feed([]byte("abc\r\n"))
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 700, 400)
	tester.SetFocused(true)
	tester.Frame()
	tester.Key(ui.Cmd, ui.KeyF)
	tester.Type("missing")
	waitSearch(t, tester, p, 0, 0)
	tester.Frame()
	if !tester.HasText("无结果") {
		t.Fatal("未显示无结果")
	}
	tester.Key(ui.Cmd, ui.KeyA)
	tester.Key(0, ui.KeyBackspace)
	tester.Frame()
	if p.search.query != "" || p.terminal.SearchStatus().Text != "" {
		t.Fatalf("空查询未清除高亮: %+v", p.search)
	}
}

func TestWorkspaceShortcutsWhileSearchFocused(t *testing.T) {
	w, conns := searchWorkspace(t)
	a, _ := w.NewTab(true)
	b, _ := w.NewTab(false)
	c, _ := w.NewTab(false)
	w.closeTab(b.TabID)
	d, err := w.NewTab(false)
	if err != nil {
		t.Fatal(err)
	}
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 760, 480)
	tester.SetFocused(true)
	tester.Frame()
	w.mu.Lock()
	w.focusRequest = a.SessionID
	w.mu.Unlock()
	tester.Key(ui.Cmd, ui.KeyF)
	tester.Frame()
	if !tester.Focused("搜索当前窗格") {
		t.Fatal("待处理的终端焦点抢走搜索输入")
	}
	tester.SetClipboard("关键词😀")
	tester.Key(ui.Cmd, ui.KeyV)
	tester.Frame()
	if w.panes[a.SessionID].search.query != "关键词😀" {
		t.Fatalf("粘贴未进入搜索框: %q", w.panes[a.SessionID].search.query)
	}
	tester.Key(ui.Cmd, ui.Key2)
	tester.Frame()
	current, err := w.CurrentSession()
	if err != nil || current.SessionID != c.SessionID {
		t.Fatalf("Cmd+2 未按位置切换: %+v %v", current, err)
	}
	if w.panes[a.SessionID].search.open {
		t.Fatal("切换标签未关闭原搜索")
	}
	tester.Key(ui.Cmd, ui.Key9)
	tester.Frame()
	current, _ = w.CurrentSession()
	if current.SessionID != c.SessionID {
		t.Fatal("不存在位置改变了焦点")
	}
	tester.Key(ui.Cmd, ui.KeyF)
	tester.Frame()
	if !tester.Focused("搜索当前窗格") {
		t.Fatal("切换后无法打开搜索")
	}
	tester.Type("test")
	tester.Key(ui.Cmd, ui.KeyD)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := w.ListSessions()
		if len(list.Sessions) == 4 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	tester.Frame()
	list, _ := w.ListSessions()
	current, _ = w.CurrentSession()
	if len(list.Sessions) != 4 || current.TabID != c.TabID || current.SessionID == c.SessionID {
		t.Fatalf("搜索框中 Cmd+D 未分屏: %+v %+v", list, current)
	}
	if w.panes[c.SessionID].search.open {
		t.Fatal("分屏后原搜索未关闭")
	}
	if err := w.FocusSession(d.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	for id, conn := range conns {
		if got := conn.contents(); got != "" {
			t.Fatalf("搜索或快捷键进入会话 %s: %q", id, got)
		}
	}
}

func TestSearchUnicodeWrappedAlternateAndResize(t *testing.T) {
	w, _ := searchWorkspace(t)
	a, _ := w.NewTab(true)
	p := w.panes[a.SessionID]
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 480, 300)
	tester.SetFocused(true)
	tester.Frame()
	cols, _ := p.terminal.Size()
	if cols < 8 {
		t.Fatalf("终端列数过少: %d", cols)
	}
	p.terminal.Feed([]byte(strings.Repeat("x", cols-2) + "ab中文😀\r\n"))
	tester.Key(ui.Cmd, ui.KeyF)
	tester.Type("ab中文😀")
	waitSearch(t, tester, p, 1, 1)
	tester.SetSize(380, 280)
	waitSearch(t, tester, p, 1, 1)
	p.terminal.Feed([]byte("\x1b[?1049h\x1b[H\x1b[2Jab中文😀\r\nab中文😀"))
	waitSearch(t, tester, p, 2, 1)
	p.terminal.Feed([]byte("\x1b[?1049l"))
	waitSearch(t, tester, p, 1, 1)
	p.terminal.Feed([]byte("\r\nab中文😀"))
	waitSearch(t, tester, p, 2, 2)
}

func TestSearchExcludesEvictedScrollback(t *testing.T) {
	w, _ := searchWorkspaceLimit(t, 32768)
	a, _ := w.NewTab(true)
	p := w.panes[a.SessionID]
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 500, 300)
	tester.SetFocused(true)
	tester.Frame()
	p.terminal.Feed([]byte("eviction-target\r\n" + strings.Repeat("0123456789abcdefghijklmnop\r\n", 6000)))
	tester.Key(ui.Cmd, ui.KeyF)
	tester.Type("eviction-target")
	waitSearch(t, tester, p, 0, 0)
	tester.Frame()
	if !tester.HasText("无结果") {
		t.Fatal("淘汰内容仍被搜索或没有显示无结果")
	}
}

func TestSearchClickOtherPaneAndCloseOldCallback(t *testing.T) {
	w, _ := searchWorkspace(t)
	a, _ := w.NewTab(true)
	b, _ := w.SplitSession(a.SessionID, "right", false)
	old, other := w.panes[a.SessionID], w.panes[b.SessionID]
	old.terminal.Feed([]byte("target\r\n"))
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 760, 480)
	tester.SetFocused(true)
	tester.Frame()
	tester.Key(ui.Cmd, ui.KeyF)
	tester.Type("target")
	waitSearch(t, tester, old, 1, 1)
	if err := tester.Click("终端 " + b.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	current, _ := w.CurrentSession()
	if current.SessionID != b.SessionID || old.search.open || old.search.query != "target" {
		t.Fatalf("点击窗格后状态错误: %+v %+v", current, old.search)
	}
	if err := w.FocusSession(a.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	tester.Key(ui.Cmd, ui.KeyF)
	waitSearch(t, tester, old, 1, 1)
	w.remove(a.SessionID, nil)
	tester.Frame()
	if old.search.open || old.terminal.SearchStatus().Text != "" {
		t.Fatalf("关闭窗格未清理搜索: %+v", old.search)
	}
	before := other.search
	old.searchStatus(terminal.SearchStatus{Text: "target", Total: 12, Selected: 1})
	if old.search.open || other.search != before {
		t.Fatalf("旧回调修改了存活窗格: old=%+v other=%+v", old.search, other.search)
	}
	current, err := w.CurrentSession()
	if err != nil || current.SessionID != b.SessionID {
		t.Fatalf("关闭后焦点错误: %+v %v", current, err)
	}
}
