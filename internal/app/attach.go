// 本文件协调远端接入的窗口确认。
package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/egoist/mygo/ui"
)

type attachConfirmation struct {
	update     func(func())
	open       bool
	pending    chan bool
	sessionID  string
	tabID      string
	focus      func(string)
	autoAttach func() bool
	recovery   bool
}

func (c controller) AttachAuto(ctx context.Context, shellName string, notify func() error) error {
	if c.confirmation == nil {
		return errors.New("窗口不支持远端接入确认")
	}
	if c.pane == nil || c.pane.ctx == nil {
		return errors.New("目标会话不可用")
	}
	return c.attachAutoWith(ctx, shellName, notify, c.Session.AttachAuto)
}

func (c controller) attachAutoWith(ctx context.Context, shellName string, notify func() error, attach func(context.Context, string, func(context.Context) error) error) error {
	auto := c.confirmation.autoAttach != nil && c.confirmation.autoAttach()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.pane.ctx, cancel)
	defer stop()
	return attach(ctx, shellName, func(ctx context.Context) error {
		if auto {
			return nil
		}
		if err := notify(); err != nil {
			return err
		}
		return c.confirmation.askTarget(ctx, c.pane.id, c.pane.tabID)
	})
}

// ask 等待用户确认或请求取消；界面状态只在窗口线程中修改。
func (a *attachConfirmation) ask(ctx context.Context) error { return a.askTarget(ctx, "", "") }

func (a *attachConfirmation) askTarget(ctx context.Context, sessionID, tabID string) error {
	return a.askTargetMode(ctx, sessionID, tabID, false)
}

func (a *attachConfirmation) askTargetMode(ctx context.Context, sessionID, tabID string, recovery bool) error {
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
		a.sessionID, a.tabID = sessionID, tabID
		a.recovery = recovery
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
	message := "请确认目标终端已登录目标服务器，并停在远端 Bash 的空提示符。确认后，程序将自动输入接入命令。"
	if a.recovery {
		message = "请确认目标终端已停在远端 Bash 的空提示符。恢复将放弃等待旧请求，其结果仍然未知；不会重放命令或中断连接。"
	}
	if a.sessionID != "" {
		message += fmt.Sprintf("\n会话: %s\n标签页: %s", a.sessionID, a.tabID)
	}
	buttons := []string{"确认接入", "取消"}
	if a.sessionID != "" {
		buttons = []string{"确认接入", "查看目标", "取消"}
	}
	choice := ui.AlertDialog(c, &a.open, "确认远端接入", message, buttons...)
	switch choice {
	case 0:
		a.respond(true)
	case 1:
		if a.sessionID != "" {
			a.open = true
			if a.focus != nil {
				a.focus(a.sessionID)
			}
		} else {
			a.respond(false)
		}
	case 2, ui.AlertDismissed:
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
