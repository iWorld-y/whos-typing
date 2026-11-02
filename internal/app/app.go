// Package app 创建终端窗口并管理应用生命周期。
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"whos-typing/internal/control"
	"whos-typing/internal/shell"
)

type controller struct {
	*shell.Session
	*session
	confirmation *attachConfirmation
}

// Run 启动应用并等待退出。终端或应用启动失败时返回原因。
func Run() error {
	server, err := control.Listen()
	if err != nil {
		return err
	}
	started := false
	stopUpdates := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		if !started {
			go server.Serve(nil)
		}
		_ = server.Close()
		close(stopUpdates)
	}()
	s := newSession()
	mygo.App.WhenReady(func() {
		if err := terminal.Load(); err != nil {
			s.setError(fmt.Errorf("加载终端失败: %w", err))
			mygo.App.Quit()
			return
		}
		sh, err := shell.New(server.Dir())
		if err != nil {
			s.setError(err)
			mygo.App.Quit()
			return
		}
		term, err := terminal.New(terminal.Options{
			Conn: sh, OnExit: s.onExit, OnKey: sh.OnKey,
			OnPaste: func(transfer.Data) bool { return sh.OnPaste() },
		})
		if err != nil {
			_ = sh.Close()
			s.setError(fmt.Errorf("创建终端失败: %w", err))
			mygo.App.Quit()
			return
		}
		sh.SetSender(term.Send)

		confirmation := &attachConfirmation{}
		controlView := &controlView{control: sh}
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:  "Who's Typing",
			Width:  760,
			Height: 480,
			Content: ui.View(func(c *ui.Context) {
				controlView.view(c, func() { terminal.View(c, term).Fill().AutoFocus() })
				confirmation.view(c)
			}),
		})
		confirmation.update = win.Update
		win.OnClosed(func() {
			s.close()
			mygo.App.Quit()
		})
		state := make(chan shell.ControlOwner, 1)
		sh.SetControlHandler(func(owner shell.ControlOwner) {
			select {
			case state <- owner:
			default:
				select {
				case <-state:
				default:
				}
				select {
				case state <- owner:
				default:
				}
			}
		})
		go func() {
			for {
				select {
				case owner := <-state:
					win.Update(func() {
						controlView.owner = owner
						controlView.message = ""
					})
				case <-stopUpdates:
					return
				}
			}
		}()
		go server.Serve(controller{Session: sh, session: s, confirmation: confirmation})
		started = true
		s.setReady(term, win)
		go installBundledCLI(ctx, win)
	})

	err = mygo.App.Run()
	s.close()
	return errors.Join(err, s.error())
}
