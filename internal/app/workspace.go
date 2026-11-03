// 本文件维护标签页、分屏树及会话生命周期。
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"whos-typing/internal/control"
)

type pane struct {
	id, tabID string
	initial   bool
	target    control.Executor
	terminal  *terminal.Terminal
	life      *session
	view      *controlView
	search    paneSearch
	focus     ui.Handle
	ctx       context.Context
	cancel    context.CancelFunc
	closeFn   func()
	closeOnce sync.Once
}

func (p *pane) close() {
	p.closeOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		if p.closeFn != nil {
			p.closeFn()
		}
	})
}

type layoutNode struct {
	id            string
	paneID        string
	direction     string
	percent       float32
	first, second *layoutNode
}

type workspaceTab struct {
	id    string
	title string
	root  *layoutNode
	last  string
}

type paneFactory func(string, func(int)) (*pane, error)

type workspace struct {
	mu            sync.Mutex
	shutdownOnce  sync.Once
	tabs          []*workspaceTab
	panes         map[string]*pane
	selected      string
	focusRequest  string
	terminalFocus string
	blurPane      *pane
	searchBlur    []*pane
	stopping      bool
	factory       paneFactory
	ids           *idAllocator
	notify        func(bool)
	windowFocused func() bool
	onEmpty       func()
	onError       func(*pane, error)
}

func newWorkspace(ids *idAllocator, factory paneFactory) *workspace {
	return &workspace{panes: make(map[string]*pane), factory: factory, ids: ids}
}

func randomID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("生成会话标识失败: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

func leaf(id string) *layoutNode { return &layoutNode{id: id, paneID: id} }

func (n *layoutNode) find(id string) *layoutNode {
	if n == nil {
		return nil
	}
	if n.paneID == id {
		return n
	}
	if found := n.first.find(id); found != nil {
		return found
	}
	return n.second.find(id)
}

func (n *layoutNode) firstPane() string {
	if n == nil {
		return ""
	}
	if n.paneID != "" {
		return n.paneID
	}
	if id := n.first.firstPane(); id != "" {
		return id
	}
	return n.second.firstPane()
}

// removeLeaf 删除叶节点并以兄弟子树替换父节点，返回应接续焦点的会话。
func removeLeaf(n *layoutNode, id string) (*layoutNode, string, bool) {
	if n == nil {
		return nil, "", false
	}
	if n.paneID == id {
		return nil, "", true
	}
	if n.paneID != "" {
		return n, "", false
	}
	first, focus, removed := removeLeaf(n.first, id)
	if removed {
		if first == nil {
			return n.second, n.second.firstPane(), true
		}
		n.first = first
		return n, focus, true
	}
	second, focus, removed := removeLeaf(n.second, id)
	if removed {
		if second == nil {
			return n.first, n.first.firstPane(), true
		}
		n.second = second
		return n, focus, true
	}
	return n, "", false
}

func (w *workspace) tabLocked(id string) *workspaceTab {
	for _, t := range w.tabs {
		if t.id == id {
			return t
		}
	}
	return nil
}

func (w *workspace) currentLocked() string {
	if t := w.tabLocked(w.selected); t != nil {
		return t.last
	}
	return ""
}

func (w *workspace) markFocusLocked(id string) {
	p := w.panes[id]
	if p == nil {
		return
	}
	old := w.currentLocked()
	if old != "" && old != id && w.panes[old] != nil {
		w.searchBlur = append(w.searchBlur, w.panes[old])
	}
	if old != "" && old != id && w.panes[old] != nil && w.panes[old].tabID != p.tabID {
		w.blurPane = w.panes[old]
	}
	w.selected = p.tabID
	w.tabLocked(p.tabID).last = id
	w.focusRequest = id
}

func (w *workspace) changed(focus bool) {
	if w.notify != nil {
		w.notify(focus)
	}
}

// create 先创建资源，再提交布局；回调等待提交结束以防早退和迟到回调。
func (w *workspace) create(target, direction string, focus bool) (control.CreatedSession, error) {
	w.mu.Lock()
	if w.stopping {
		w.mu.Unlock()
		return control.CreatedSession{}, errors.New("工作区已关闭")
	}
	var tabID string
	if target != "" {
		p := w.panes[target]
		if p == nil {
			w.mu.Unlock()
			return control.CreatedSession{}, errors.New("目标会话不存在")
		}
		if direction != "right" && direction != "down" {
			w.mu.Unlock()
			return control.CreatedSession{}, errors.New("分屏方向必须是 right 或 down")
		}
		tabID = p.tabID
	}
	w.mu.Unlock()
	id, err := w.ids.next(sessionKind)
	if err != nil {
		return control.CreatedSession{}, err
	}
	if tabID == "" {
		tabID, err = w.ids.next(tabKind)
		if err != nil {
			return control.CreatedSession{}, err
		}
	}
	var splitID string
	if target != "" {
		splitID, err = randomID()
		if err != nil {
			return control.CreatedSession{}, err
		}
	}
	ready := make(chan struct{})
	var p *pane
	onExit := func(code int) {
		go func() {
			<-ready
			if p != nil {
				w.exited(p, code)
			}
		}()
	}
	p, err = w.factory(id, onExit)
	if err != nil {
		if p != nil {
			p.close()
		}
		close(ready)
		return control.CreatedSession{}, err
	}
	if p == nil {
		close(ready)
		return control.CreatedSession{}, errors.New("创建会话失败")
	}
	p.id, p.tabID = id, tabID
	w.mu.Lock()
	if w.stopping {
		err = errors.New("工作区已关闭")
	} else if p.life != nil && p.life.isDone() {
		err = errors.New("新会话已退出")
	} else if target != "" {
		existing := w.panes[target]
		tab := w.tabLocked(tabID)
		if existing == nil || existing.tabID != tabID || tab == nil {
			err = errors.New("目标会话已关闭")
		} else if node := tab.root.find(target); node == nil {
			err = errors.New("目标会话已关闭")
		} else {
			*node = layoutNode{id: splitID, direction: direction, percent: 50, first: leaf(target), second: leaf(id)}
			if !focus && tabID == w.selected && w.terminalFocus != "" {
				w.focusRequest = w.terminalFocus
			}
		}
	} else {
		w.tabs = append(w.tabs, &workspaceTab{id: tabID, root: leaf(id), last: id})
	}
	if err == nil {
		p.initial = len(w.panes) == 0 && len(w.tabs) == 1
		w.panes[id] = p
		if w.selected == "" || focus {
			w.markFocusLocked(id)
		}
	}
	w.mu.Unlock()
	close(ready)
	if err != nil {
		p.close()
		return control.CreatedSession{}, err
	}
	w.changed(focus)
	return control.CreatedSession{SessionID: id, TabID: tabID}, nil
}

func (w *workspace) NewTab(focus bool) (control.CreatedSession, error) {
	return w.create("", "", focus)
}
func (w *workspace) SplitSession(id, direction string, focus bool) (control.CreatedSession, error) {
	return w.create(id, direction, focus)
}
func (w *workspace) Execute(context.Context, string) control.Result {
	return control.Result{Err: errors.New("必须先解析会话")}
}

func (w *workspace) ResolveSession(id string) (string, control.Executor, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if id == "" {
		switch len(w.panes) {
		case 0:
			return "", nil, errors.New("没有可用会话")
		case 1:
			for key := range w.panes {
				id = key
			}
		default:
			return "", nil, errors.New("存在多个会话，请使用 session list 并指定 --session")
		}
	}
	p := w.panes[id]
	if p == nil {
		return "", nil, errors.New("目标会话不存在或已关闭")
	}
	return id, p.target, nil
}

func (p *pane) info(tabTitle string) control.SessionInfo {
	title := "终端"
	if p.terminal != nil {
		if name := p.terminal.Title(); name != "" {
			title = name
		}
	}
	owner := "human"
	if manager, ok := p.target.(control.ControlManager); ok {
		owner = manager.ControlStatus()
	}
	return control.SessionInfo{SessionID: p.id, TabID: p.tabID, TabTitle: tabTitle, Title: title, Control: owner}
}

func (w *workspace) ListSessions() (control.SessionList, error) {
	w.mu.Lock()
	list := control.SessionList{Sessions: make([]control.SessionInfo, 0, len(w.panes)), SessionID: w.currentLocked()}
	for _, tab := range w.tabs {
		var walk func(*layoutNode)
		walk = func(n *layoutNode) {
			if n == nil {
				return
			}
			if n.paneID != "" {
				if p := w.panes[n.paneID]; p != nil {
					list.Sessions = append(list.Sessions, p.info(w.tabTitleLocked(tab)))
				}
				return
			}
			walk(n.first)
			walk(n.second)
		}
		walk(tab.root)
	}
	focused := w.windowFocused
	w.mu.Unlock()
	if focused != nil {
		list.WindowFocused = focused()
	}
	return list, nil
}
func (w *workspace) CurrentSession() (control.CurrentSession, error) {
	w.mu.Lock()
	p := w.panes[w.currentLocked()]
	if p == nil {
		w.mu.Unlock()
		return control.CurrentSession{}, errors.New("没有当前会话")
	}
	current := control.CurrentSession{SessionInfo: p.info(w.tabTitleLocked(w.tabLocked(p.tabID)))}
	focused := w.windowFocused
	w.mu.Unlock()
	if focused != nil {
		current.WindowFocused = focused()
	}
	return current, nil
}

// RenameTab 更新指定标签页名称，不改变当前焦点。
func (w *workspace) RenameTab(id, title string) error {
	if strings.TrimSpace(title) == "" {
		return errors.New("标签页名称不能为空")
	}
	w.mu.Lock()
	tab := w.tabLocked(id)
	if tab == nil {
		w.mu.Unlock()
		return errors.New("标签页不存在或已关闭")
	}
	tab.title = title
	w.mu.Unlock()
	w.changed(false)
	return nil
}

func (w *workspace) FocusSession(id string) error {
	w.mu.Lock()
	if w.panes[id] == nil {
		w.mu.Unlock()
		return errors.New("目标会话不存在或已关闭")
	}
	w.markFocusLocked(id)
	w.mu.Unlock()
	w.changed(true)
	return nil
}

// remove 从注册表和布局移除会话，再在锁外回收资源。
func (w *workspace) remove(id string, expected *pane) {
	w.mu.Lock()
	p := w.panes[id]
	if p == nil || (expected != nil && p != expected) {
		w.mu.Unlock()
		return
	}
	w.searchBlur = append(w.searchBlur, p)
	delete(w.panes, id)
	tab := w.tabLocked(p.tabID)
	if tab == nil {
		w.mu.Unlock()
		p.close()
		return
	}
	var next string
	tab.root, next, _ = removeLeaf(tab.root, id)
	wasCurrent := w.selected == tab.id && tab.last == id
	if tab.root == nil {
		index := 0
		for i, t := range w.tabs {
			if t == tab {
				index = i
				break
			}
		}
		w.tabs = append(w.tabs[:index], w.tabs[index+1:]...)
		if w.selected == tab.id {
			w.selected = ""
			if len(w.tabs) > 0 {
				if index >= len(w.tabs) {
					index = len(w.tabs) - 1
				}
				w.selected = w.tabs[index].id
				w.focusRequest = w.tabs[index].last
			}
		}
	} else if tab.last == id {
		if next == "" {
			next = tab.root.firstPane()
		}
		tab.last = next
		if wasCurrent {
			w.focusRequest = next
		}
	}
	empty := len(w.panes) == 0 && !w.stopping
	if empty {
		w.stopping = true
	}
	onEmpty := w.onEmpty
	w.mu.Unlock()
	p.close()
	w.changed(false)
	if empty && onEmpty != nil {
		onEmpty()
	}
}

func (w *workspace) exited(p *pane, code int) {
	if code == -1 {
		w.mu.Lock()
		if w.panes[p.id] != p {
			w.mu.Unlock()
			return
		}
		err := errors.New("启动 Shell 失败")
		if p.life != nil && p.life.error() != nil {
			err = p.life.error()
		}
		if w.onError != nil {
			w.onError(p, err)
		}
		w.mu.Unlock()
	}
	w.remove(p.id, p)
}

func (w *workspace) closeTab(id string) {
	w.mu.Lock()
	index := -1
	for i, tab := range w.tabs {
		if tab.id == id {
			index = i
			break
		}
	}
	if index < 0 {
		w.mu.Unlock()
		return
	}
	var panes []*pane
	for key, p := range w.panes {
		if p.tabID == id {
			panes = append(panes, p)
			w.searchBlur = append(w.searchBlur, p)
			delete(w.panes, key)
		}
	}
	w.tabs = append(w.tabs[:index], w.tabs[index+1:]...)
	if w.selected == id {
		w.selected = ""
		if len(w.tabs) > 0 {
			if index >= len(w.tabs) {
				index = len(w.tabs) - 1
			}
			w.selected = w.tabs[index].id
			w.focusRequest = w.tabs[index].last
		}
	}
	empty := len(w.panes) == 0 && !w.stopping
	if empty {
		w.stopping = true
	}
	onEmpty := w.onEmpty
	w.mu.Unlock()
	for _, p := range panes {
		p.close()
	}
	w.changed(false)
	if empty && onEmpty != nil {
		onEmpty()
	}
}

func (w *workspace) shutdown() {
	w.shutdownOnce.Do(func() {
		w.mu.Lock()
		w.stopping = true
		panes := make([]*pane, 0, len(w.panes))
		for _, p := range w.panes {
			panes = append(panes, p)
		}
		w.panes = make(map[string]*pane)
		w.tabs = nil
		w.selected = ""
		w.mu.Unlock()
		for _, p := range panes {
			p.close()
		}
	})
}
