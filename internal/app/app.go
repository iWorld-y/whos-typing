// Package app 创建终端窗口并管理应用生命周期。
package app

import (
	"errors"
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// Run 启动应用并等待退出。终端或应用启动失败时返回原因。
func Run() error {
	s := newSession()
	mygo.App.WhenReady(func() {
		term, err := terminal.New(terminal.Options{OnExit: s.onExit})
		if err != nil {
			s.setError(fmt.Errorf("创建终端失败: %w", err))
			mygo.App.Quit()
			return
		}

		win := mygo.NewWindow(mygo.WindowOptions{
			Title:  "Who's Typing",
			Width:  760,
			Height: 480,
			Content: ui.View(func(c *ui.Context) {
				terminal.View(c, term).Fill().AutoFocus()
			}),
		})
		win.OnClosed(func() {
			s.close()
			mygo.App.Quit()
		})
		s.setReady(term, win)
	})

	err := mygo.App.Run()
	s.close()
	return errors.Join(err, s.error())
}
