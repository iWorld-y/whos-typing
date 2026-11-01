package app

import (
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"whos-typing/internal/shell"
)

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
