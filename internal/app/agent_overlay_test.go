package app

import (
	"bytes"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

func TestAgentOverlayCompilesAndDraws(t *testing.T) {
	effect, err := shader.Compile(agentOverlaySource)
	if err != nil {
		t.Fatalf("编译 Agent 粒子着色器失败: %v", err)
	}
	render := func(draw bool) []byte {
		image := ui.Render(func(c *ui.Context) {
			root := ui.Box(c).Fill().Background(ui.Hex("#111827"))
			if draw {
				root.DrawOver(func(p *ui.Painter, r ui.Rect) {
					effect.Draw(p, r, shader.Options{Uniforms: map[string]any{
						"Time": 0.0, "Opacity": 1.0,
						"Pointer": []float32{0, 0}, "PointerAge": 100.0,
					}})
				})
			}
		}, 80, 60, 1)
		return image.Pix
	}
	if bytes.Equal(render(false), render(true)) {
		t.Fatal("粒子着色器未改变终端区域的绘制结果")
	}
}
