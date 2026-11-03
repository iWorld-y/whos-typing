// 本文件绘制工作区标签栏和递归分屏。
package app

import (
	"fmt"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type workspaceView struct {
	workspace *workspace
	update    func(func())
	message   string
	ratios    map[string]*float32
}

func (v *workspaceView) report(err error) {
	if err == nil || v.update == nil {
		return
	}
	v.update(func() { v.message = err.Error() })
}

func (v *workspaceView) create(target, direction string) {
	go func() {
		var err error
		if target == "" {
			_, err = v.workspace.NewTab(true)
		} else {
			_, err = v.workspace.SplitSession(target, direction, true)
		}
		v.report(err)
	}()
}

func (v *workspaceView) splitCurrent(direction string) {
	v.workspace.mu.Lock()
	id := v.workspace.currentLocked()
	v.workspace.mu.Unlock()
	if id != "" {
		v.create(id, direction)
	}
}

func (v *workspaceView) view(c *ui.Context) {
	w := v.workspace
	if w == nil {
		return
	}
	c.OnShortcut(ui.Cmd, ui.KeyT, func() { v.create("", "") })
	c.OnShortcut(ui.Cmd, ui.KeyD, func() { v.splitCurrent("right") })
	c.OnShortcut(ui.Cmd|ui.Shift, ui.KeyD, func() { v.splitCurrent("down") })
	c.OnShortcut(ui.Cmd, ui.KeyF, v.openSearch)
	for i, key := range []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9} {
		index := i
		c.OnShortcut(ui.Cmd, key, func() { v.selectTab(index) })
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	v.pruneRatios(w)
	for _, p := range w.searchBlur {
		p.endSearch()
	}
	w.searchBlur = nil
	if p := w.panes[w.currentLocked()]; p != nil && p.search.open {
		c.OnShortcut(ui.Cmd, ui.KeyG, func() { v.navigateSearch(false) })
		c.OnShortcut(ui.Cmd|ui.Shift, ui.KeyG, func() { v.navigateSearch(true) })
		c.OnShortcut(0, ui.KeyEscape, func() { v.closeSearch(p) })
	}
	ui.Column(c).Fill().Children(func() {
		v.tabBar(c, w)
		if v.message != "" {
			ui.Text(c.Key("workspace-error"), v.message).TextColor(ui.Hex("#cc3333"))
		}
		request := w.focusRequest
		actual := ""
		if tab := w.tabLocked(w.selected); tab != nil {
			ui.Box(c.Key("workspace-content")).FillWidth().Grow(1).Clip().Children(func() {
				v.node(c, tab.root, request, &actual)
			})
		}
		if actual != "" && request == "" && actual != w.currentLocked() {
			p := w.panes[actual]
			if p != nil {
				if old := w.panes[w.currentLocked()]; old != nil {
					old.endSearch()
				}
				w.selected = p.tabID
				w.tabLocked(p.tabID).last = actual
			}
		}
		w.terminalFocus = actual
		if request != "" {
			w.focusRequest = ""
		}
		// 只构建一次隐藏视图，让上游终端发送失焦事件并结束输入法组合。
		if p := w.blurPane; p != nil {
			w.blurPane = nil
			if w.panes[p.id] == p && p.tabID != w.selected && p.terminal != nil {
				ui.Box(c.Key("blur-"+p.id)).Absolute().Size(0, 0).Clip().IgnoreInput(true).Children(func() { terminal.View(c, p.terminal).Size(0, 0) })
			}
		}
	})
}

// selectTab 按当前位置切换，不把快捷键中的数字当作标识。
func (v *workspaceView) selectTab(index int) {
	w := v.workspace
	w.mu.Lock()
	if index < 0 || index >= len(w.tabs) {
		w.mu.Unlock()
		return
	}
	w.markFocusLocked(w.tabs[index].last)
	w.mu.Unlock()
	w.changed(true)
}

func (v *workspaceView) tabMenu(e ui.Element, w *workspace, tab *workspaceTab) {
	tabID, paneID := tab.id, tab.last
	e.ContextMenu(func(m *ui.Menu) {
		if m.Item("关闭当前窗格").Chosen() {
			go w.remove(paneID, nil)
		}
		if m.Item("关闭标签页").Chosen() {
			go w.closeTab(tabID)
		}
	})
}

// tabBar 在系统窗口按钮以外绘制标题或等宽标签。
func (v *workspaceView) tabBar(c *ui.Context, w *workspace) {
	bar := c.TitleBar()
	height := max(bar.Height, 32)
	ui.Row(c.Key("tabs-row")).FillWidth().Height(height).Padding(0, bar.Right+8, 0, bar.Left+8).AlignItems(ui.Center).DragWindow().Children(func() {
		if len(w.tabs) == 0 {
			return
		}
		if len(w.tabs) == 1 {
			item := w.tabs[0]
			title := w.tabTitleLocked(item)
			box := ui.Row(c.Key("single-title")).FillWidth().Height(height).MinWidth(0).AlignItems(ui.Center).DragWindow()
			v.tabMenu(box, w, item)
			box.Children(func() { ui.Text(c, title).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0).Tooltip(title) })
			return
		}
		selected := 0
		for i, tab := range w.tabs {
			if tab.id == w.selected {
				selected = i
				break
			}
		}
		old := selected
		tabs := ui.TabsBase(c.Key("workspace-tabs"), &selected, len(w.tabs))
		tabs.List.FillWidth().Height(height).MinWidth(0).Children(func() {
			for i, item := range w.tabs {
				title := w.tabTitleLocked(item)
				c.Key("tab-" + item.id)
				tab := tabs.Tab(i).WidthPercent(100/float32(len(w.tabs))).MinWidth(0).Shrink(1).Height(height).Padding(0, 8).AlignItems(ui.Center).Label(title).Tooltip(title)
				v.tabMenu(tab, w, item)
				if i == selected {
					tab.FontWeight(600)
				} else {
					tab.Background(c.Theme().Surface)
				}
				tab.Children(func() { ui.Text(c, title).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0) })
			}
		})
		if tabs.List.Changed() && selected != old && selected >= 0 && selected < len(w.tabs) {
			w.markFocusLocked(w.tabs[selected].last)
		}
	})
}

func (v *workspaceView) node(c *ui.Context, n *layoutNode, request string, actual *string) {
	if n == nil {
		return
	}
	if n.paneID != "" {
		p := v.workspace.panes[n.paneID]
		if p == nil || p.terminal == nil || p.view == nil {
			return
		}
		ui.Column(c.Key("pane-" + p.id)).Fill().Children(func() {
			if p.search.open {
				v.searchBar(c, p)
			}
			ui.Box(c.Key("pane-terminal-area-" + p.id)).FillWidth().Grow(1).MinHeight(0).Children(func() {
				p.view.view(c, func() {
					e := terminal.View(c.Key("terminal-"+p.id), p.terminal).Label(fmt.Sprintf("终端 %s", p.id)).Fill().Bind(&p.focus)
					if request == p.id {
						e.Focus()
					}
					if e.Focused() {
						*actual = p.id
					}
				})
			})
		})
		return
	}
	first := func() { v.node(c, n.first, request, actual) }
	second := func() { v.node(c, n.second, request, actual) }
	if v.ratios == nil {
		v.ratios = make(map[string]*float32)
	}
	ratio := v.ratios[n.id]
	if ratio == nil {
		value := n.percent
		ratio = &value
		v.ratios[n.id] = ratio
	}
	var split ui.Element
	if n.direction == "down" {
		split = ui.SplitVerticalPercent(c.Key("split-"+n.id), ratio, first, second).Fill()
	} else {
		split = ui.SplitPercent(c.Key("split-"+n.id), ratio, first, second).Fill()
	}
	// MyGo 的拖动回调可能在视图返回后执行，因此比例指针只由 UI 线程使用。
	// 持工作区锁时再将比例复制到布局。
	split.Changed()
	n.percent = *ratio
}

func (v *workspaceView) pruneRatios(w *workspace) {
	if len(v.ratios) == 0 {
		return
	}
	active := make(map[string]bool)
	var walk func(*layoutNode)
	walk = func(n *layoutNode) {
		if n == nil || n.paneID != "" {
			return
		}
		active[n.id] = true
		walk(n.first)
		walk(n.second)
	}
	for _, tab := range w.tabs {
		walk(tab.root)
	}
	for id := range v.ratios {
		if !active[id] {
			delete(v.ratios, id)
		}
	}
}
