// 本文件管理各窗格的搜索状态及搜索栏。
package app

import (
	"fmt"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// paneSearch 仅由窗口 UI 线程访问。
type paneSearch struct {
	query              string
	open, focusRequest bool
	status             terminal.SearchStatus
}

func (p *pane) endSearch() {
	if !p.search.open {
		return
	}
	p.search.open = false
	p.search.focusRequest = false
	p.search.status = terminal.SearchStatus{}
	if p.terminal != nil {
		p.terminal.EndSearch()
	}
}

func (p *pane) searchStatus(status terminal.SearchStatus) {
	if p.search.open && status.Text == p.search.query {
		p.search.status = status
		// 搜索栏改变终端尺寸后，上游可能保留匹配数但清除选中项。
		if status.Total > 0 && status.Selected == 0 && !status.Searching && p.terminal != nil {
			p.terminal.SearchNext()
		}
	}
}

func (v *workspaceView) openSearch() {
	w := v.workspace
	w.mu.Lock()
	p := w.panes[w.currentLocked()]
	if p != nil {
		w.focusRequest = ""
	}
	w.mu.Unlock()
	if p == nil || p.terminal == nil {
		return
	}
	if p.search.open {
		p.search.focusRequest = true
		return
	}
	p.search.open = true
	p.search.focusRequest = true
	p.search.status = terminal.SearchStatus{Text: p.search.query, Searching: p.search.query != ""}
	p.terminal.Search(p.search.query)
}

func (v *workspaceView) closeSearch(p *pane) {
	p.endSearch()
	_ = v.workspace.FocusSession(p.id)
}

func (v *workspaceView) navigateSearch(previous bool) {
	w := v.workspace
	w.mu.Lock()
	p := w.panes[w.currentLocked()]
	w.mu.Unlock()
	if p == nil || !p.search.open || p.terminal == nil {
		return
	}
	if previous {
		p.terminal.SearchPrevious()
	} else {
		p.terminal.SearchNext()
	}
}

func (v *workspaceView) searchBar(c *ui.Context, p *pane) {
	ui.Row(c.Key("search-bar-"+p.id)).FillWidth().Padding(4, 8).Gap(6).AlignItems(ui.Center).Children(func() {
		input := ui.TextInput(c.Key("search-input-"+p.id), &p.search.query).Label("搜索当前窗格").Placeholder("搜索终端").Grow(1).MinWidth(0)
		if p.search.focusRequest {
			input.Focus()
			p.search.focusRequest = false
		}
		input.OnChange(func() {
			if !p.search.open {
				return
			}
			p.search.status = terminal.SearchStatus{Text: p.search.query, Searching: p.search.query != ""}
			p.terminal.Search(p.search.query)
		})
		input.OnTextKey(func(key ui.TextKey) bool {
			if key.Key != ui.KeyEnter || (key.Mods != 0 && key.Mods != ui.Shift) {
				return false
			}
			v.navigateSearch(key.Mods == ui.Shift)
			return true
		})
		status := ""
		if p.search.query != "" {
			switch {
			case p.search.status.Text != p.search.query || p.search.status.Searching:
				status = "搜索中"
			case p.search.status.Total == 0:
				status = "无结果"
			default:
				status = fmt.Sprintf("%d / %d", p.search.status.Selected, p.search.status.Total)
			}
		}
		ui.Text(c.Key("search-status-"+p.id), status).SingleLine().Shrink(0)
		if ui.Button(c.Key("search-next-"+p.id), "↑").Label("较早结果").KeepFocus().Clicked() {
			p.terminal.SearchNext()
		}
		if ui.Button(c.Key("search-previous-"+p.id), "↓").Label("较新结果").KeepFocus().Clicked() {
			p.terminal.SearchPrevious()
		}
		ui.Button(c.Key("search-close-"+p.id), "×").Label("关闭搜索").KeepFocus().OnClick(func() { v.closeSearch(p) })
	})
}
