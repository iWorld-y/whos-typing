// 本文件在软件渲染时绘制低开销的蓝色光带和动态粒子。
package app

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// drawSoftwareOverlay 避免逐像素执行着色器，同时保留控制状态提示。
func drawSoftwareOverlay(p *ui.Painter, r ui.Rect, opacity float32, elapsed float64, pointerX, pointerY float32, pointerAge float64) {
	p.Fill(r, ui.RGBA(138, 153, 184, 0.05*opacity), 0)
	width := min(min(r.W, r.H)/2, min(max(min(r.W, r.H)*0.18, 70), 130))
	strength := float32(0.25+0.08*math.Sin(elapsed*0.7)) * opacity
	blue, clear := ui.RGBA(46, 122, 250, strength), ui.RGBA(46, 122, 250, 0)
	p.FillGradient(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: width}, ui.LinearGradient{From: blue, To: clear, Angle: 180}, 0)
	p.FillGradient(ui.Rect{X: r.X, Y: r.Y + r.H - width, W: r.W, H: width}, ui.LinearGradient{From: blue, To: clear, Angle: 0}, 0)
	p.FillGradient(ui.Rect{X: r.X, Y: r.Y, W: width, H: r.H}, ui.LinearGradient{From: blue, To: clear, Angle: 90}, 0)
	p.FillGradient(ui.Rect{X: r.X + r.W - width, Y: r.Y, W: width, H: r.H}, ui.LinearGradient{From: blue, To: clear, Angle: 270}, 0)

	for y := float32(6); y < r.H; y += 12 {
		for x := float32(6); x < r.W; x += 12 {
			seed := math.Sin(float64(x)*12.9898+float64(y)*78.233) * 43758.5453
			random := seed - math.Floor(seed)
			wave := 0.5 + 0.5*math.Sin(float64(x+y)*0.025-elapsed*1.2)
			brightness := 0.10 + 0.28*random + 0.15*wave
			if pointerAge < 3 {
				dx, dy := float64(x-pointerX), float64(y-pointerY)
				brightness += 0.35 * math.Exp(-(dx*dx+dy*dy)/8500-pointerAge*1.3)
			}
			if random < 0.35+0.2*wave {
				p.Fill(ui.Rect{X: r.X + x, Y: r.Y + y, W: 1, H: 1}, ui.RGBA(250, 252, 255, float32(brightness)*opacity), 0)
			}
		}
	}
}
