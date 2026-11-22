package shell

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"whos-typing/internal/control"
)

func TestPasteEncoding(t *testing.T) {
	text := " 中文\t一\r\n\n  二\n"
	if got := string(encodePaste(text, PastePlain)); got != " 中文\t一\r\r  二\r" {
		t.Fatalf("普通粘贴：%q", got)
	}
	if got := string(encodePaste(text, PasteBracketed)); got != "\x1b[200~"+text+"\x1b[201~" {
		t.Fatalf("括号粘贴：%q", got)
	}
}
func pasteAwait[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case result := <-c:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("粘贴通道超时")
	}
	var zero T
	return zero
}
func TestPasteModeDowngradeRequiresConfirmation(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	queued, done, confirm := make(chan []byte, 2), make(chan error, 1), make(chan struct{}, 1)
	f.s.SetSender(func(p []byte) { queued <- p })
	var mode atomic.Uint32
	mode.Store(uint32(PasteBracketed))
	go func() {
		done <- f.s.PasteText(ctx, "printf 'WT_%s' 'RECEIVED'\n", func() PasteTarget { return PasteTarget{Mode: PasteMode(mode.Load())} }, func(context.Context) error { confirm <- struct{}{}; return nil })
	}()
	first := pasteAwait(t, queued)
	select {
	case <-confirm:
		t.Fatal("初始括号粘贴不应请求确认")
	default:
	}
	mode.Store(uint32(PastePlain))
	if _, err := f.s.Write(first); err != nil {
		t.Fatal(err)
	}
	pasteAwait(t, confirm)
	second := pasteAwait(t, queued)
	if strings.Contains(f.screen(), "WT_RECEIVED") {
		t.Fatal("尚未写就报告程序输出")
	}
	if _, err := f.s.Write(second); err != nil {
		t.Fatal(err)
	}
	if err := pasteAwait(t, done); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_RECEIVED")
}
func TestPasteInvalidatedByHumanInputOrCancellation(t *testing.T) {
	for _, cause := range []string{"keys", "cancel", "takeover", "output", "close", "password", "dirty"} {
		t.Run(cause, func(t *testing.T) {
			f := newFixture(t, "/bin/zsh")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			confirm, done := make(chan struct{}, 1), make(chan error, 1)
			queued := make(chan []byte, 1)
			f.s.SetSender(func(p []byte) { queued <- p })
			if cause == "takeover" {
				f.s.mu.Lock()
				f.s.setControl(ControlAgent)
				f.s.mu.Unlock()
			}
			if cause == "dirty" {
				f.s.OnKey(0, ui.KeyA)
			}
			go func() {
				done <- f.s.PasteText(ctx, "MUST_NOT_WRITE\n", func() PasteTarget { return PasteTarget{Mode: PasteUnknown, PasswordPrompt: cause == "password"} }, func(ctx context.Context) error { confirm <- struct{}{}; <-ctx.Done(); return ctx.Err() })
			}()
			if cause != "dirty" && cause != "password" {
				pasteAwait(t, confirm)
			}
			switch cause {
			case "keys":
				f.s.OnKey(0, ui.KeyA)
			case "cancel":
				cancel()
			case "takeover":
				f.s.TakeOver()
			case "output":
				f.s.mu.Lock()
				f.s.notePasteOutput()
				f.s.mu.Unlock()
			case "close":
				_ = f.s.Close()
			}
			err := pasteAwait(t, done)
			if err == nil || cause == "keys" && !errors.Is(err, errPasteChanged) {
				t.Fatalf("拒绝原因：%v", err)
			}
			select {
			case <-queued:
				t.Fatal("拒绝的请求进入队列")
			default:
			}
		})
	}
}

func TestPasteCanFeedRunningAgentCommand(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result := make(chan control.Result, 1)
	go func() {
		result <- f.s.Execute(ctx, "printf 'WT_%s\\n' WAITING; read -r value; printf 'WT_%s:%s\\n' OK \"$value\"")
	}()
	waitText(t, f, "WT_WAITING")
	confirmed := false
	err := f.s.PasteText(ctx, "answer\n", func() PasteTarget { return PasteTarget{Mode: PastePlain} }, func(context.Context) error { confirmed = true; return nil })
	if err != nil || !confirmed {
		t.Fatalf("等待输入的 Agent 命令：确认 %v，错误 %v", confirmed, err)
	}
	r := pasteAwait(t, result)
	if r.Err != nil || r.Output == nil {
		t.Fatalf("原请求未正常完成：%+v", r)
	}
	defer os.Remove(r.Output.Name())
	defer r.Output.Close()
	out, err := io.ReadAll(r.Output)
	if err != nil || !strings.Contains(string(out), "WT_OK:answer") {
		t.Fatalf("原请求结果：%q %v", out, err)
	}
}

func TestPasteCancellationAfterPartialWriteIsUnknown(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	s := &Session{pty: write}
	queued := make(chan []byte, 1)
	s.SetSender(func(p []byte) { queued <- p })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- s.PasteText(ctx, strings.Repeat("a", 64<<10), func() PasteTarget { return PasteTarget{Mode: PasteBracketed} }, func(context.Context) error { return nil })
	}()
	barrier := pasteAwait(t, queued)
	select {
	case <-done:
		t.Fatal("尚未写入就返回结果")
	default:
	}
	writeDone := make(chan struct{})
	go func() { defer close(writeDone); _, _ = s.Write(barrier) }()
	if err := read.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(read, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := pasteAwait(t, done); !errors.Is(err, control.ErrPasteUnknown) {
		t.Fatalf("已部分写入：%v", err)
	}
	pasteAwait(t, writeDone)
	select {
	case <-queued:
		t.Fatal("未知状态下自动重试")
	default:
	}
	if s.SessionDiagnostic().Unknown {
		t.Fatal("粘贴结果未知不应伪造命令结果未知")
	}
}
