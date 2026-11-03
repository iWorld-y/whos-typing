// Package app 创建终端窗口并管理应用生命周期。
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"whos-typing/internal/control"
	"whos-typing/internal/shell"
)

type controller struct {
	*shell.Session
	*session
	confirmation *attachConfirmation
	pane         *pane
}

type appError struct {
	mu  sync.Mutex
	err error
}

func (e *appError) set(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err == nil {
		e.err = err
	}
}
func (e *appError) get() error { e.mu.Lock(); defer e.mu.Unlock(); return e.err }

// Run 启动应用并等待退出。终端或应用启动失败时返回原因。
func Run() error {
	server, err := control.Listen()
	if err != nil {
		return err
	}
	started := false
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		if !started {
			go server.Serve(nil)
		}
		_ = server.Close()
	}()
	var startup appError
	var ws *workspace
	mygo.App.WhenReady(func() {
		if err := terminal.Load(); err != nil {
			startup.set(fmt.Errorf("加载终端失败: %w", err))
			mygo.App.Quit()
			return
		}
		confirmation := &attachConfirmation{}
		var view workspaceView
		win := mygo.NewWindow(mygo.WindowOptions{Title: "Who's Typing", Width: 760, Height: 480, Hidden: true,
			Content: ui.View(func(c *ui.Context) { view.view(c); confirmation.view(c) }),
		})
		ws = newWorkspace(func(id string, onExit func(int)) (*pane, error) {
			return makePane(server.Dir(), win, confirmation, ws, id, onExit)
		})
		view.workspace = ws
		view.update = win.Update
		confirmation.update = win.Update
		confirmation.focus = func(id string) { _ = ws.FocusSession(id) }
		ws.notify = func(focus bool) {
			win.Update(func() {
				if focus {
					win.Focus()
				}
			})
		}
		var focused atomic.Bool
		win.OnFocus(func() { focused.Store(true) })
		win.OnBlur(func() { focused.Store(false) })
		ws.windowFocused = focused.Load
		ws.onEmpty = win.Close
		ws.onError = func(p *pane, err error) {
			if p.initial {
				startup.set(err)
			} else {
				view.report(err)
			}
		}
		win.OnClosed(func() { go func() { ws.shutdown(); mygo.App.Quit() }() })
		if _, err := ws.NewTab(false); err != nil {
			startup.set(err)
			win.Close()
			return
		}
		go server.Serve(ws)
		started = true
		win.Show()
		go installBundledCLI(ctx, win)
	})
	err = mygo.App.Run()
	if ws != nil {
		ws.shutdown()
	}
	return errors.Join(err, startup.get())
}
