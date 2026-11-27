// 本文件创建受管 SSH 窗格：终端绑定远端 PTY，执行经独立控制通道。
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"

	"whos-typing/internal/shell"
	"whos-typing/internal/sshlink"
)

// managedController 把受管会话接入窗格的执行、控制权和粘贴入口。
// 不实现原有远端接入：受管会话失效时提示显式新建，而不是恢复提示符接入。
type managedController struct {
	*shell.ManagedSession
	*session
	pane   *pane
	update func(func())
}

func (c managedController) Paste(ctx context.Context, text string, notify func() error) error {
	if c.pane == nil || c.pane.ctx == nil || c.pane.terminal == nil || c.update == nil {
		return errors.New("目标窗格不可用")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.pane.ctx, cancel)
	defer stop()
	term := c.pane.terminal
	return c.ManagedSession.PasteText(ctx, text, func() shell.PasteTarget {
		target := pasteMode(term.Snapshot())
		target.PasswordPrompt = isPasswordPrompt(term.Text())
		return target
	}, func(wait context.Context) error {
		return confirmPanePaste(wait, c.pane, c.update, text, notify)
	})
}

// makeManagedPane 创建受管 SSH 会话、终端和控制器；失败时回收本次资源，不留下空布局。
func makeManagedPane(dir string, win *mygo.Window, workspace *workspace, id string, onExit func(int), target, config string) (*pane, error) {
	link, err := sshlink.New(target, config)
	if err != nil {
		return nil, err
	}
	sh, err := shell.NewManaged(dir, link)
	if err != nil {
		return nil, err
	}
	life := newSession()
	life.exit = onExit
	ctx, cancel := context.WithCancel(context.Background())
	p := &pane{id: id, life: life, ctx: ctx, cancel: cancel, view: &controlView{control: sh}}
	term, err := terminal.New(terminal.Options{
		Conn: sh, OnExit: life.onExit, OnKey: sh.OnKey,
		OnPaste: func(transfer.Data) bool { return sh.OnPaste() },
		OnTitle: func(string) { win.Update(func() {}) },
		OnSearch: func(status terminal.SearchStatus) {
			p.searchStatus(status)
			win.Update(func() {})
		},
	})
	if err != nil {
		cancel()
		_ = sh.Close()
		return nil, fmt.Errorf("创建终端失败: %w", err)
	}
	p.terminal = term
	p.closeFn = life.close
	p.target = managedController{ManagedSession: sh, session: life, pane: p, update: win.Update}
	sh.SetTitleHandler(func() { win.Update(func() {}) })
	sh.SetControlHandler(func(owner shell.ControlOwner) {
		win.Update(func() {
			workspace.mu.Lock()
			defer workspace.mu.Unlock()
			if workspace.panes[id] == p {
				p.view.owner = owner
				p.view.message = ""
			}
		})
	})
	life.setReady(term, nil)
	return p, nil
}
