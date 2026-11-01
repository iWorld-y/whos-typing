// 本文件绘制终端控制栏及 Agent 控制时的粒子效果。
package app

import (
	"time"

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
	control   terminalControl
	owner     shell.ControlOwner
	started   time.Time
	pointerX  float32
	pointerY  float32
	pointerAt time.Time
	message   string
}

// view 显示终端内容，并根据控制方绘制特效和操作栏。
func (v *controlView) view(c *ui.Context, content func()) {
	if v.started.IsZero() {
		v.started = c.Now()
	}
	root := ui.Column(c).Fill().Background(ui.Hex("#111827"))
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
			v.controlBar(c)
		}
	})
}

func (v *controlView) controlBar(c *ui.Context) {
	ui.Row(c).FillWidth().Padding(8, 12).Background(ui.Hex("#1b2636")).Children(func() {
		ui.Row(c).FillWidth().MinHeight(44).Padding(8, 14).Gap(12).
			AlignItems(ui.Center).Radius(12).Background(ui.Hex("#354458")).Children(func() {
			if v.owner == shell.ControlAgent {
				ui.Text(c, "agent 正在控制").FontSize(14).Bold().TextColor(ui.Hex("#67acff"))
			} else {
				ui.Text(c, "人类正在控制").FontSize(14).TextColor(ui.Hex("#d7e2f0"))
			}
			if v.message != "" {
				ui.Text(c, v.message).FontSize(12).TextColor(ui.Hex("#ffd6b0"))
			}
			ui.Spacer(c).Grow(1)
			if v.owner == shell.ControlAgent {
				if ui.Button(c.Key("take-over"), "接管").KeepFocus().Clicked() {
					v.message = ""
					v.control.TakeOver()
				}
			} else if ui.Button(c.Key("return-control"), "交还 Agent").KeepFocus().Clicked() {
				if err := v.control.ReturnControl(); err != nil {
					v.message = err.Error()
				} else {
					v.message = ""
				}
			}
		})
	})
}
