// 本文件协调远端接入的窗口确认。
package app

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
)

type attachConfirmation struct {
	update  func(func())
	open    bool
	pending chan bool
}

func (c controller) AttachAuto(ctx context.Context, shellName string, notify func() error) error {
	if c.confirmation == nil {
		return errors.New("窗口不支持远端接入确认")
	}
	return c.Session.AttachAuto(ctx, shellName, func(ctx context.Context) error {
		if err := notify(); err != nil {
			return err
		}
		return c.confirmation.ask(ctx)
	})
}

// ask 等待用户确认或请求取消；界面状态只在窗口线程中修改。
func (a *attachConfirmation) ask(ctx context.Context) error {
	result := make(chan bool, 1)
	a.update(func() {
		if ctx.Err() != nil {
			return
		}
		if a.pending != nil {
			result <- false
			return
		}
		a.pending = result
		a.open = true
	})
	defer a.update(func() {
		if a.pending == result {
			a.pending = nil
			a.open = false
		}
	})
	select {
	case approved := <-result:
		if !approved {
			return errors.New("用户取消远端接入")
		}
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *attachConfirmation) view(c *ui.Context) {
	if !a.open {
		return
	}
	choice := ui.AlertDialog(c, &a.open, "确认远端接入",
		"请确认当前终端已登录目标服务器，并停在远端 Bash 的空提示符。确认后，程序将自动输入接入命令。",
		"确认接入", "取消")
	switch choice {
	case 0:
		a.respond(true)
	case 1, ui.AlertDismissed:
		a.respond(false)
	}
}

func (a *attachConfirmation) respond(approved bool) {
	result := a.pending
	a.pending = nil
	a.open = false
	if result != nil {
		result <- approved
	}
}
