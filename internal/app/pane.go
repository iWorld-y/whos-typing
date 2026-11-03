// 本文件创建并回收每个终端会话的资源。
package app

import (
	"context"
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"

	"whos-typing/internal/shell"
)

// makePane 创建独立 Shell、终端和控制器，并在失败时回收已有资源。
func makePane(dir string, win *mygo.Window, confirmation *attachConfirmation, workspace *workspace, id string, onExit func(int)) (*pane, error) {
	sh, err := shell.New(dir)
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
	p.target = controller{Session: sh, session: life, confirmation: confirmation, pane: p}
	p.view.recoverRemote = func() {
		if p.view.recovering {
			return
		}
		p.view.recovering = true
		p.view.message = "等待确认远端 Bash 空提示符"
		go func() {
			err := sh.RecoverRemote(ctx, func(ctx context.Context) error {
				return confirmation.askTarget(ctx, p.id, p.tabID)
			})
			win.Update(func() {
				p.view.recovering = false
				if err != nil {
					p.view.message = err.Error()
				} else {
					p.view.message = ""
					p.view.recoverable = false
				}
			})
		}()
	}
	sh.SetTitleHandler(func() { win.Update(func() {}) })
	sh.SetSender(term.Send)
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
