// 本文件绘制终端控制栏及 Agent 控制时的粒子效果。
package app

import (
	"errors"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
	"whos-typing/internal/shell"
)

var agentOverlay = shader.MustCompile(agentOverlaySource)

type terminalControl interface {
	TakeOver()
	ReturnControl() error
}

type controlView struct {
	control       terminalControl
	owner         shell.ControlOwner
	started       time.Time
	pointerX      float32
	pointerY      float32
	pointerAt     time.Time
	message       string
	recoverRemote func()
	recoverable   bool
	recovering    bool
}

// view 显示终端内容，并根据控制方绘制特效和操作栏。
func (v *controlView) view(c *ui.Context, content func()) {
	if v.started.IsZero() {
		v.started = c.Now()
	}
	root := ui.Column(c).Fill().Clip().Background(ui.Hex("#111827"))
	root.Children(func() {
		area := ui.Box(c.Key("terminal-area")).FillWidth().Grow(1)
		var target float32
		if v.owner == shell.ControlAgent {
			target = 1
		}
		opacity := area.Animate("agent-overlay-opacity", target, 500*time.Millisecond)
		if x, y, over := area.PointerPosition(); over && (v.pointerAt.IsZero() || x != v.pointerX || y != v.pointerY) {
			v.pointerX, v.pointerY, v.pointerAt = x, y, c.Now()
		}
		pointerX, pointerY, pointerAt := v.pointerX, v.pointerY, v.pointerAt
		reduceMotion := c.Preferences().ReduceMotion
		area.DrawOver(func(p *ui.Painter, r ui.Rect) {
			if opacity <= 0 {
				return
			}
			now := p.Now()
			elapsed := now.Sub(v.started).Seconds()
			if reduceMotion {
				elapsed = 0
			}
			age := 100.0
			if !pointerAt.IsZero() {
				age = now.Sub(pointerAt).Seconds()
			}
			fps := float32(30)
			if p.Accelerated() {
				agentOverlay.Draw(p, r, shader.Options{Uniforms: map[string]any{
					"Time": elapsed, "Opacity": opacity,
					"Pointer": []float32{pointerX, pointerY}, "PointerAge": age,
				}})
			} else {
				drawSoftwareOverlay(p, r, opacity, elapsed, pointerX, pointerY, age)
				fps = 15
			}
			if !reduceMotion {
				p.AnimationFrameRate(fps)
			}
		})
		area.Children(content)
		if v.owner != shell.ControlHuman {
			width := area.Bounds().W
			if width <= 0 {
				width, _ = c.Size()
			}
			v.controlBar(c, width)
		}
	})
}

func (v *controlView) controlBar(c *ui.Context, width float32) {
	textColor, agentColor, errorColor := ui.Hex("#081a2c"), ui.Hex("#0b2137"), ui.Hex("#241408")
	buttonColor := ui.RGBA(255, 255, 255, 0.10)
	if c.Theme().Dark {
		textColor, agentColor, errorColor = ui.Hex("#f6faff"), ui.Hex("#f6faff"), ui.Hex("#fff8f0")
		buttonColor = ui.RGBA(0, 0, 0, 0.10)
	}
	ui.Column(c.Key("control-capsule")).Role(ui.RoleGroup).Label("终端控制").Attach(ui.AnchorTop, ui.AnchorTop).Top(10).
		MaxWidth(min(420, max(0, width-24))).Padding(6, 12).Gap(4).Radius(24).
		Material(glass.Glass{Tint: ui.RGBA(44, 77, 112, 0.30)}).
		Shadow(0, 3, 8, 0, ui.RGBA(16, 27, 48, 0.15)).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Wrap().Children(func() {
			key, label := "take-over", "接管"
			if v.owner == shell.ControlAgent {
				ui.Text(c, "agent 正在控制").FontSize(14).Bold().TextColor(agentColor).SingleLine().Shrink(0).Translate(0, -0.5)
			} else {
				key, label = "return-control", "交还 Agent"
				ui.Text(c, "人类正在控制").FontSize(14).TextColor(textColor).SingleLine().Shrink(0).Translate(0, -0.5)
			}
			button := ui.Button(c.Key(key), "").Label(label).KeepFocus().Shrink(0)
			if button.Pressed() {
				buttonColor.A = min(255, buttonColor.A+26)
			} else if button.Hovered() {
				buttonColor.A = min(255, buttonColor.A+13)
			}
			button.Background(buttonColor).BorderWidth(0).Radius(10).TextColor(textColor).Children(func() {
				ui.Text(c, label).SingleLine().Translate(0, -1)
			})
			if button.Clicked() {
				v.recoverable = false
				if v.owner == shell.ControlAgent {
					v.message = ""
					v.control.TakeOver()
				} else if err := v.control.ReturnControl(); err != nil {
					v.message = err.Error()
					v.recoverable = errors.Is(err, shell.ErrRemoteNotReady)
				} else {
					v.message = ""
				}
			}
		})
		showRecovery := v.recoverable
		if status, ok := v.control.(interface{ CanShowRemoteRecovery() bool }); ok && status.CanShowRemoteRecovery() {
			showRecovery = true
		}
		if v.owner == shell.ControlHumanTakeover && showRecovery && v.recoverRemote != nil && !v.recovering {
			if ui.Button(c.Key("recover-remote"), "恢复远端接入").KeepFocus().Clicked() {
				v.recoverRemote()
			}
		}
		if v.message != "" {
			ui.Text(c, v.message).FontSize(12).TextColor(errorColor)
		}
	})
}
