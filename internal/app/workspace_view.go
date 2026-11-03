// 本文件绘制工作区标签栏和递归分屏。
package app

import (
	"fmt"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type workspaceView struct {
	workspace   *workspace
	update      func(func())
	message     string
	tabsScroll  ui.ScrollState
	revealedTab string
	ratios      map[string]*float32
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
	w.mu.Lock()
	defer w.mu.Unlock()
	v.pruneRatios(w)
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

// tabBar 保留滚动入口，并在选中标签变化时显示该标签。
func (v *workspaceView) tabBar(c *ui.Context, w *workspace) {
	ui.Row(c.Key("tabs-row")).FillWidth().AlignItems(ui.Center).Children(func() {
		if ui.Button(c.Key("tabs-left"), "‹").Label("向左滚动标签").KeepFocus().Clicked() {
			v.tabsScroll.X -= 180
		}
		ui.ScrollHorizontal(c.Key("tabs-scroll")).Grow(1).Shrink(1).MinWidth(0).Height(36).TrackScroll(&v.tabsScroll).Scrollbars(true).Children(func() {
			selected := 0
			for i, tab := range w.tabs {
				if tab.id == w.selected {
					selected = i
					break
				}
			}
			old := selected
			tabs := ui.TabsBase(c.Key("workspace-tabs"), &selected, len(w.tabs))
			tabs.List.Gap(4).Children(func() {
				for i, item := range w.tabs {
					label := item.title
					runes := []rune(label)
					if len(runes) > 20 {
						label = string(runes[:20]) + "…"
					}
					c.Key("tab-" + item.id)
					tab := tabs.Tab(i).Padding(6, 12).Radius(6)
					tabID, paneID := item.id, item.last
					tab.ContextMenu(func(m *ui.Menu) {
						if m.Item("关闭当前窗格").Chosen() {
							go w.remove(paneID, nil)
						}
						if m.Item("关闭标签页").Chosen() {
							go w.closeTab(tabID)
						}
					})
					if i == selected {
						tab.Background(c.Theme().Surface).FontWeight(600)
						if item.id != v.revealedTab {
							tab.ScrollIntoView()
							v.revealedTab = item.id
						}
					}
					tab.Children(func() { ui.Text(c, label).SingleLine() })
				}
			})
			if tabs.List.Changed() && selected != old && selected >= 0 && selected < len(w.tabs) {
				w.markFocusLocked(w.tabs[selected].last)
			}
		})
		if ui.Button(c.Key("tabs-right"), "›").Label("向右滚动标签").KeepFocus().Clicked() {
			v.tabsScroll.X += 180
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
		c.Key("pane-" + p.id)
		p.view.view(c, func() {
			e := terminal.View(c.Key("terminal-"+p.id), p.terminal).Label(fmt.Sprintf("终端 %s", p.id)).Fill().Bind(&p.focus)
			if request == p.id {
				e.Focus()
			}
			if e.Focused() {
				*actual = p.id
			}
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
