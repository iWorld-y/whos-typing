package app

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestAttachConfirmationButtons(t *testing.T) {
	for _, tc := range []struct {
		name    string
		button  string
		confirm bool
	}{
		{"确认", "确认接入", true},
		{"取消", "取消", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := make(chan bool, 1)
			prompt := &attachConfirmation{open: true, pending: result}
			tester := ui.NewTester(func(c *ui.Context) {
				ui.Button(c, "模拟终端").AutoFocus()
				prompt.view(c)
			}, 760, 480)
			tester.Frame()
			if !tester.HasText("确认远端接入") {
				t.Fatal("未显示远端接入确认")
			}
			if err := tester.Click(tc.button); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-result:
				if got != tc.confirm {
					t.Fatalf("确认结果为 %v，期望 %v", got, tc.confirm)
				}
			default:
				t.Fatal("点击后未返回确认结果")
			}
			if prompt.open {
				t.Fatal("点击后确认窗口未关闭")
			}
		})
	}
}

func TestAttachConfirmationCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prompt := &attachConfirmation{update: func(fn func()) { fn() }}
	if err := prompt.ask(ctx); err == nil {
		t.Fatal("已取消的请求不应被确认")
	}
	if prompt.open || prompt.pending != nil {
		t.Fatal("请求取消后仍显示确认窗口")
	}
}

func TestAttachConfirmationKeyboardCancels(t *testing.T) {
	for _, key := range []ui.Key{ui.KeyEscape, ui.KeyEnter} {
		result := make(chan bool, 1)
		prompt := &attachConfirmation{open: true, pending: result}
		tester := ui.NewTester(func(c *ui.Context) {
			ui.Button(c, "模拟终端").AutoFocus()
			prompt.view(c)
		}, 760, 480)
		tester.Frame()
		tester.Key(0, key)
		select {
		case approved := <-result:
			if approved {
				t.Fatalf("按键 %v 不应默认确认远端接入", key)
			}
		default:
			t.Fatalf("按键 %v 未取消远端接入", key)
		}
	}
}

func TestAttachConfirmationCancellationBeforeWindowUpdate(t *testing.T) {
	updates := make(chan func(), 2)
	prompt := &attachConfirmation{update: func(fn func()) { updates <- fn }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- prompt.ask(ctx) }()
	show := <-updates
	cancel()
	if err := <-done; err == nil {
		t.Fatal("取消请求应返回错误")
	}
	show()
	(<-updates)()
	if prompt.open || prompt.pending != nil {
		t.Fatal("延迟执行的窗口更新不应重新打开确认框")
	}
}
