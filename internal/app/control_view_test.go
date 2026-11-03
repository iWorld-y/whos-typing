package app

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"whos-typing/internal/shell"
)

func TestControlViewCapsuleFloatsAtTopCenter(t *testing.T) {
	view := &controlView{owner: shell.ControlHuman}
	view.control = &fakeViewControl{view: view}
	var text string
	tester := ui.NewTester(func(c *ui.Context) {
		view.view(c, func() { ui.TextInput(c, &text).Label("测试终端").Fill() })
	}, 320, 180)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	humanTerminal, ok := tester.Find("测试终端")
	if !ok {
		t.Fatal("未找到终端")
	}
	view.owner = shell.ControlAgent
	tester.Frame()
	for _, size := range []struct{ width, height int }{{320, 180}, {760, 480}} {
		tester.SetSize(size.width, size.height)
		terminal, ok := tester.Find("测试终端")
		if !ok || terminal.Y != 0 || terminal.H != float32(size.height) {
			t.Fatalf("悬浮胶囊不应占用终端空间: 窗口=%+v 终端=%+v", size, terminal)
		}
		status, statusOK := tester.Find("agent 正在控制")
		button, buttonOK := tester.Find("接管")
		capsule, capsuleOK := tester.Find("终端控制")
		center := capsule.X + capsule.W/2
		if !statusOK || !buttonOK || !capsuleOK || status.Y >= 40 || button.Y >= 40 || math.Abs(float64(center-float32(size.width)/2)) > 2 {
			t.Fatalf("状态与按钮应在顶部居中: 窗口=%+v 胶囊=%+v 状态=%+v 按钮=%+v", size, capsule, status, button)
		}
	}
	if humanTerminal.H != 180 {
		t.Fatalf("人类控制时终端应填充窗口: %+v", humanTerminal)
	}
	view.owner = shell.ControlHuman
	tester.Frame()
	if tester.HasText("agent 正在控制") || tester.HasText("接管") {
		t.Fatal("普通人类控制时应隐藏胶囊")
	}
}

type fakeViewControl struct {
	view      *controlView
	returnErr error
}

func (f *fakeViewControl) TakeOver() { f.view.owner = shell.ControlHumanTakeover }

func (f *fakeViewControl) ReturnControl() error {
	if f.returnErr != nil {
		return f.returnErr
	}
	f.view.owner = shell.ControlAgent
	return nil
}

func TestControlViewTakeOverAndReturn(t *testing.T) {
	view := &controlView{owner: shell.ControlHuman}
	controller := &fakeViewControl{view: view}
	view.control = controller
	tester := ui.NewTester(func(c *ui.Context) {
		view.view(c, func() { ui.Text(c, "终端内容") })
	}, 320, 160)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	view.owner = shell.ControlAgent
	tester.Frame()
	if !tester.HasText("agent 正在控制") || !tester.HasText("接管") || tester.HasText("终止任务") {
		t.Fatalf("Agent 控制栏内容错误: %v", tester.Texts())
	}
	if err := tester.Click("接管"); err != nil {
		t.Fatal(err)
	}
	tester.Advance(500 * time.Millisecond)
	if view.owner != shell.ControlHumanTakeover || !tester.HasText("交还 Agent") || tester.HasText("agent 正在控制") {
		t.Fatalf("接管后控制栏错误: %v", tester.Texts())
	}
	controller.returnErr = errors.New("终端尚未就绪")
	if err := tester.Click("交还 Agent"); err != nil {
		t.Fatal(err)
	}
	if view.owner != shell.ControlHumanTakeover || !tester.HasText("终端尚未就绪") {
		t.Fatalf("交还失败应显示原因: %v", tester.Texts())
	}
	controller.returnErr = nil
	if err := tester.Click("交还 Agent"); err != nil {
		t.Fatal(err)
	}
	tester.Advance(500 * time.Millisecond)
	if view.owner != shell.ControlAgent || !tester.HasText("agent 正在控制") || tester.HasText("交还 Agent") {
		t.Fatalf("交还后控制栏错误: %v", tester.Texts())
	}
}

func TestControlViewRemoteRecoveryRequiresExplicitClick(t *testing.T) {
	view := &controlView{owner: shell.ControlHumanTakeover}
	view.control = &fakeViewControl{view: view, returnErr: shell.ErrRemoteNotReady}
	called := 0
	view.recoverRemote = func() { called++; view.recovering = true }
	tester := ui.NewTester(func(c *ui.Context) {
		view.view(c, func() { ui.Text(c, "终端内容") })
	}, 320, 240)
	tester.Frame()
	if tester.HasText("重新接入") {
		t.Fatal("交还失败前不应显示恢复入口")
	}
	if err := tester.Click("交还 Agent"); err != nil {
		t.Fatal(err)
	}
	if !tester.HasText("重新接入") || called != 0 || view.owner != shell.ControlHumanTakeover {
		t.Fatal("交还失败只能提供恢复入口，不能自动重新接入")
	}
	if err := tester.Click("重新接入"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	_, recoverButton := tester.Find("重新接入")
	if called != 1 || recoverButton || view.owner != shell.ControlHumanTakeover {
		t.Fatalf("恢复期间应保持人类控制并避免重复发起: called=%d button=%v owner=%v", called, recoverButton, view.owner)
	}
}

func TestControlViewCapsuleErrorFitsNarrowWindow(t *testing.T) {
	for _, width := range []int{180, 240, 320} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			view := &controlView{owner: shell.ControlHuman}
			controller := &fakeViewControl{view: view}
			view.control = controller
			var text string
			tester := ui.NewTester(func(c *ui.Context) {
				view.view(c, func() { ui.TextInput(c, &text).Label("测试终端").Fill().AutoFocus() })
			}, width, 180)
			tester.SetPreferences(ui.Preferences{ReduceMotion: true})
			view.owner = shell.ControlAgent
			tester.Frame()
			tester.SetSize(320, 180)
			fullAgentStatus, ok := tester.Find("agent 正在控制")
			if !ok {
				t.Fatal("未找到 Agent 控制提示")
			}
			tester.SetSize(width, 180)
			agentStatus, ok := tester.Find("agent 正在控制")
			if !ok || agentStatus.W+0.5 < fullAgentStatus.W {
				t.Fatalf("窄窗口不应截断 Agent 控制提示: 完整=%+v 当前=%+v", fullAgentStatus, agentStatus)
			}
			if err := tester.Click("接管"); err != nil {
				t.Fatal(err)
			}
			tester.SetSize(320, 180)
			fullHumanStatus, ok := tester.Find("人类正在控制")
			if !ok {
				t.Fatal("未找到人类控制提示")
			}
			tester.SetSize(width, 180)
			humanStatus, ok := tester.Find("人类正在控制")
			if !ok || humanStatus.W+0.5 < fullHumanStatus.W {
				t.Fatalf("窄窗口不应截断人类控制提示: 完整=%+v 当前=%+v", fullHumanStatus, humanStatus)
			}
			controller.returnErr = errors.New("终端尚未就绪，请完成当前输入或等待前台程序")
			if err := tester.Click("交还 Agent"); err != nil {
				t.Fatal(err)
			}
			capsule, capsuleOK := tester.Find("终端控制")
			button, buttonOK := tester.Find("交还 Agent")
			message, messageOK := tester.Find(controller.returnErr.Error())
			if !capsuleOK || !buttonOK || !messageOK || capsule.X < 0 || capsule.X+capsule.W > float32(width) || capsule.Y < 0 || capsule.Y+capsule.H > 180 ||
				button.X < capsule.X || button.X+button.W > capsule.X+capsule.W || button.Y < capsule.Y || button.Y+button.H > capsule.Y+capsule.H ||
				message.X < capsule.X || message.X+message.W > capsule.X+capsule.W || message.Y < capsule.Y || message.Y+message.H > capsule.Y+capsule.H {
				t.Fatalf("窄窗口下交还按钮和错误提示应在胶囊内: 胶囊=%+v 按钮=%+v 提示=%+v", capsule, button, message)
			}
			if !tester.Focused("测试终端") {
				t.Fatal("交还失败后终端不应失去焦点")
			}
			controller.returnErr = nil
			if err := tester.Click("交还 Agent"); err != nil {
				t.Fatal(err)
			}
			if view.owner != shell.ControlAgent {
				t.Fatal("显示错误后仍应能交还控制权")
			}
		})
	}
}

func TestControlViewCapsuleBlursBackdrop(t *testing.T) {
	view := &controlView{owner: shell.ControlHuman}
	view.control = &fakeViewControl{view: view}
	stripeX := float32(-100)
	tester := ui.NewTester(func(c *ui.Context) {
		view.view(c, func() {
			ui.Box(c).Fill().Draw(func(p *ui.Painter, r ui.Rect) {
				p.Fill(r, ui.Hex("#303030"), 0)
				p.Fill(ui.Rect{X: stripeX, Y: r.Y, W: 2, H: r.H}, ui.Hex("#ffffff"), 0)
			})
		})
	}, 320, 180)
	tester.SetDark(false)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	view.owner = shell.ControlHumanTakeover
	tester.Frame()
	capsule, ok := tester.Find("终端控制")
	if !ok {
		t.Fatal("未找到控制胶囊")
	}
	x, y := int(capsule.X+capsule.W/2), int(capsule.Y+5)
	before := tester.Image().RGBAAt(x, y)
	uncovered := tester.Image().RGBAAt(x, 100)
	stripeX = float32(x + 2)
	tester.Frame()
	after := tester.Image().RGBAAt(x, y)
	if int(after.R) <= int(before.R)+4 {
		t.Fatalf("胶囊应模糊邻近的终端内容: 原像素=%v，邻近亮条出现后=%v", before, after)
	}
	if got := tester.Image().RGBAAt(x, 100); got != uncovered {
		t.Fatalf("胶囊外的相同位置不应受邻近亮条影响: 之前=%v，之后=%v", uncovered, got)
	}
}

func TestControlViewCapsuleButtonFeedback(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprintf("dark_%v", dark), func(t *testing.T) {
			view := &controlView{owner: shell.ControlHuman}
			view.control = &fakeViewControl{view: view}
			tester := ui.NewTester(func(c *ui.Context) {
				view.view(c, func() { ui.Box(c).Fill().Background(ui.Hex("#778899")) })
			}, 320, 180)
			tester.SetDark(dark)
			tester.SetPreferences(ui.Preferences{ReduceMotion: true})
			view.owner = shell.ControlAgent
			tester.Frame()
			button, ok := tester.Find("接管")
			if !ok {
				t.Fatal("未找到接管按钮")
			}
			x, y := button.X+button.W/2, button.Y+button.H/2
			sampleX, sampleY := int(button.X+5), int(y)
			normal := tester.Image().RGBAAt(sampleX, sampleY)
			tester.Move(x, y)
			hovered := tester.Image().RGBAAt(sampleX, sampleY)
			tester.Press(x, y)
			pressed := tester.Image().RGBAAt(sampleX, sampleY)
			if normal == hovered || hovered == pressed {
				t.Fatalf("按钮应区分普通、悬停与按下状态: %v %v %v", normal, hovered, pressed)
			}
			tester.Release(x, y)
			if view.owner != shell.ControlHumanTakeover {
				t.Fatal("按钮点击后未接管终端")
			}
		})
	}
}

func TestControlViewReducedMotionDoesNotAnimate(t *testing.T) {
	view := &controlView{owner: shell.ControlHuman}
	view.control = &fakeViewControl{view: view}
	tester := ui.NewTester(func(c *ui.Context) {
		view.view(c, func() { ui.Text(c, "终端内容") })
	}, 300, 180)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	view.owner = shell.ControlAgent
	tester.Frame()
	if !tester.HasText("agent 正在控制") || tester.FramePending() {
		t.Fatal("减少动态效果时应保留控制提示并停止持续刷新")
	}
}

func TestControlViewKeepsTerminalFocusAndPointerInput(t *testing.T) {
	view := &controlView{owner: shell.ControlHuman}
	view.control = &fakeViewControl{view: view}
	var text string
	var moved bool
	var pressed bool
	var scrolled bool
	tester := ui.NewTester(func(c *ui.Context) {
		view.view(c, func() {
			ui.TextInput(c, &text).Label("测试终端").AutoFocus().HandleInput(func(ev ui.InputEvent) bool {
				switch ev.Kind {
				case ui.InputPointerMove:
					moved = true
				case ui.InputPointerDown:
					pressed = true
				case ui.InputScroll:
					scrolled = true
				}
				return false
			})
		})
	}, 320, 160)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	view.owner = shell.ControlAgent
	tester.Frame()
	tester.Move(30, 15)
	if !moved || view.pointerAt.IsZero() {
		t.Fatal("鼠标移动应到达终端并更新粒子位置")
	}
	tester.Press(30, 15)
	tester.Move(60, 15)
	tester.Release(60, 15)
	tester.Scroll(30, 15, 0, 30)
	if !pressed || !scrolled {
		t.Fatalf("特效不应拦截终端鼠标选择或滚动: pressed=%v scrolled=%v", pressed, scrolled)
	}
	tester.SetSize(500, 220)
	if !tester.HasText("接管") {
		t.Fatal("调整窗口大小后控制栏不可见")
	}
	if err := tester.Click("接管"); err != nil {
		t.Fatal(err)
	}
	if !tester.Focused("测试终端") {
		t.Fatal("接管按钮不应使终端失去输入焦点")
	}
	tester.Type("hello")
	if text != "hello" {
		t.Fatalf("接管后终端未收到输入: %q", text)
	}
}

func TestControlViewTakeOverKeepsNativeTerminalFocus(t *testing.T) {
	requireTerminal(t)
	conn := &passiveConn{closed: make(chan struct{})}
	term, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })
	view := &controlView{owner: shell.ControlHuman}
	view.control = &fakeViewControl{view: view}
	tester := ui.NewTester(func(c *ui.Context) {
		view.view(c, func() { terminal.View(c, term).Fill().AutoFocus() })
	}, 320, 180)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.SetFocused(true)
	view.owner = shell.ControlAgent
	tester.Frame()
	if !tester.Focused("Terminal") {
		t.Fatal("接管前原生终端未获得焦点")
	}
	if err := tester.Click("接管"); err != nil {
		t.Fatal(err)
	}
	if !tester.Focused("Terminal") {
		t.Fatal("接管按钮使原生终端失去焦点")
	}
	before := conn.writes.Load()
	tester.Type("from-human")
	deadline := time.Now().Add(time.Second)
	for conn.writes.Load() == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if conn.writes.Load() == before {
		t.Fatal("接管后输入未到达原生终端连接")
	}
}

func TestControlViewEffectFollowsOwner(t *testing.T) {
	view := &controlView{owner: shell.ControlHuman}
	view.control = &fakeViewControl{view: view}
	tester := ui.NewTester(func(c *ui.Context) {
		view.view(c, func() { ui.Box(c).Fill().Background(ui.Hex("#111827")) })
	}, 320, 200)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	human := tester.Image().RGBAAt(8, 8)
	view.owner = shell.ControlAgent
	tester.Frame()
	agent := tester.Image().RGBAAt(8, 8)
	if human == agent {
		t.Fatal("Agent 控制时未在终端内容上显示粒子效果")
	}
	if err := tester.Click("接管"); err != nil {
		t.Fatal(err)
	}
	if got := tester.Image().RGBAAt(8, 8); got != human {
		t.Fatalf("人类接管后粒子效果未移除: got=%v want=%v", got, human)
	}
}

func TestControlViewAttachConfirmationAboveEffect(t *testing.T) {
	view := &controlView{owner: shell.ControlHuman}
	view.control = &fakeViewControl{view: view}
	result := make(chan bool, 1)
	confirmation := &attachConfirmation{open: true, pending: result}
	tester := ui.NewTester(func(c *ui.Context) {
		view.view(c, func() { ui.Text(c, "终端内容") })
		confirmation.view(c)
	}, 400, 250)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	view.owner = shell.ControlAgent
	tester.Frame()
	if !tester.HasText("确认远端接入") || !tester.HasText("取消") {
		t.Fatalf("远端确认框未显示在控制状态上方: %v", tester.Texts())
	}
	if err := tester.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if <-result || !tester.HasText("接管") {
		t.Fatal("关闭确认框后应回到 Agent 控制栏")
	}
}

func TestTwoNarrowPanesKeepTakeoverButtonsUsable(t *testing.T) {
	left := &controlView{owner: shell.ControlAgent}
	right := &controlView{owner: shell.ControlAgent}
	left.control = &fakeViewControl{view: left}
	right.control = &fakeViewControl{view: right}
	percent := float32(50)
	tester := ui.NewTester(func(c *ui.Context) {
		ui.SplitPercent(c, &percent,
			func() { left.view(c, func() { ui.Text(c, "左侧终端") }) },
			func() { right.view(c, func() { ui.Text(c, "右侧终端") }) }).Fill()
	}, 380, 220)
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.Frame()
	button, ok := tester.Find("接管")
	if !ok || button.X < 0 || button.X+button.W > 190 {
		t.Fatalf("左侧按钮越界: %+v", button)
	}
	tester.ClickAt(button.X+button.W/2, button.Y+button.H/2)
	if left.owner != shell.ControlHumanTakeover || right.owner != shell.ControlAgent {
		t.Fatal("左侧接管影响了其他窗格")
	}
	button, ok = tester.Find("接管")
	if !ok || button.X < 190 || button.X+button.W > 380 {
		t.Fatalf("右侧按钮越界: %+v", button)
	}
	tester.ClickAt(button.X+button.W/2, button.Y+button.H/2)
	if right.owner != shell.ControlHumanTakeover {
		t.Fatal("右侧按钮不可点击")
	}
}
