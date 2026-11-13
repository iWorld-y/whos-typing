// 本文件绘制本机设置窗口。
package app

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"whos-typing/internal/settings"
)

type settingsView struct {
	store   *settings.Store
	window  *mygo.Window
	message string
}

func (v *settingsView) open() {
	if v.window != nil {
		v.window.Focus()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{Title: "设置", Width: 520, Height: 240, Content: ui.View(v.view)})
	v.window = win
	win.OnClosed(func() {
		if v.window == win {
			v.window = nil
		}
	})
	win.Show()
}

func (v *settingsView) view(c *ui.Context) {
	enabled := v.store.AutoAttach()
	ui.Column(c).Fill().Padding(20).Gap(12).Children(func() {
		ui.Text(c, "远端接入")
		if ui.Checkbox(c.Key("auto-attach"), &enabled, "自动允许 Agent 接入远端 Shell").Changed() {
			if err := v.store.SetAutoAttach(enabled); err != nil {
				v.message = err.Error()
			} else {
				v.message = ""
			}
		}
		ui.Text(c, "开启后首次接入也不弹确认框。请先确认目标已登录交互式 Bash；接管和恢复仍需单独确认。")
		if v.message != "" {
			ui.Text(c, v.message).TextColor(ui.Hex("#cc3333"))
		}
	})
}
