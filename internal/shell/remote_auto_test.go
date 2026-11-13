package shell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestAutomaticRemoteAttachAfterConfirmation(t *testing.T) {
	f := remoteFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	prompted := make(chan struct{})
	confirm := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.s.AttachAuto(ctx, "bash", func(ctx context.Context) error {
			close(prompted)
			select {
			case <-confirm:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	<-prompted
	f.s.mu.Lock()
	installed := f.s.remote.installed
	f.s.mu.Unlock()
	if installed {
		t.Fatal("用户确认前不得接入远端")
	}
	close(confirm)
	if err := <-done; err != nil {
		t.Fatalf("自动接入失败: %v", err)
	}
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("自动输入接入命令后应由 Agent 控制: %v", got)
	}
	if output, code, err := f.run(t, "printf attached"); err != nil || code != 0 || output != "attached" {
		t.Fatalf("自动接入后不能执行命令: %q %d %v", output, code, err)
	}
}

func TestAutomaticRemoteAttachRejectedDuringTakeover(t *testing.T) {
	f := remoteFixture(t)
	f.s.mu.Lock()
	f.s.setControl(ControlAgent)
	f.s.mu.Unlock()
	f.s.TakeOver()
	confirmed := false
	err := f.s.AttachAuto(context.Background(), "bash", func(context.Context) error {
		confirmed = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "人类") || confirmed {
		t.Fatalf("人类接管期间应拒绝自动接入: confirmed=%v err=%v", confirmed, err)
	}
	if strings.Contains(f.screen(), "__wt_prompt_") {
		t.Fatal("人类接管期间发送了远端接入命令")
	}
}

func TestTakeOverCancelsAutomaticAttachConfirmation(t *testing.T) {
	f := remoteFixture(t)
	f.s.mu.Lock()
	f.s.setControl(ControlAgent)
	f.s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	prompted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.s.AttachAuto(ctx, "bash", func(ctx context.Context) error {
			close(prompted)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-prompted
	f.s.TakeOver()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("人类接管后自动接入应取消")
		}
	case <-time.After(time.Second):
		t.Fatal("接管后自动接入确认仍在等待")
	}
}

func TestTakeOverCancelsQueuedAutomaticAttach(t *testing.T) {
	f := remoteFixture(t)
	f.s.mu.Lock()
	f.s.setControl(ControlAgent)
	f.s.mu.Unlock()
	queued := make(chan []byte, 1)
	f.s.SetSender(func(p []byte) { queued <- append([]byte(nil), p...) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- f.s.AttachAuto(ctx, "bash", func(context.Context) error { return nil })
	}()
	var pending []byte
	select {
	case pending = <-queued:
	case <-ctx.Done():
		t.Fatal("未收到排队的接入请求")
	}
	f.s.TakeOver()
	if _, err := f.s.Write(pending); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("人类接管后未发送的自动接入应取消")
	}
	if strings.Contains(f.screen(), "__wt_prompt_") {
		t.Fatal("接管后仍发送了自动接入命令")
	}
}

func TestAutomaticRemoteAttachHidesBootstrapEcho(t *testing.T) {
	f := remoteFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.s.AttachAuto(ctx, "bash", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("自动接入失败: %v", err)
	}
	waitText(t, f, "[CLI] 正在接入远端 Bash")
	if strings.Contains(f.screen(), "__wt_prompt_") {
		t.Fatal("终端不应显示自动输入的内部接入命令")
	}
	if output, code, err := f.run(t, "printf visible"); err != nil || code != 0 || output != "visible" {
		t.Fatalf("隐藏接入命令后普通输出异常: %q %d %v", output, code, err)
	}
}

func TestAutomaticRemoteAttachEchoFilterPreservesOtherOutput(t *testing.T) {
	r := &remoteSession{bootstrap: "eval sample"}
	var got []byte
	for _, chunk := range []string{"before\r\n", "e", "val\x1b[K ", "sample\r\n", "after\r\n"} {
		got = append(got, r.filterAttachEcho([]byte(chunk), false)...)
	}
	if string(got) != "before\r\n\r\nafter\r\n" || !r.echoDone {
		t.Fatalf("过滤后输出错误: %q", got)
	}

	r = &remoteSession{bootstrap: "eval sample"}
	got = r.filterAttachEcho([]byte("error before marker"), false)
	got = append(got, r.filterAttachEcho(nil, true)...)
	if string(got) != "error before marker" {
		t.Fatalf("不匹配的输出被丢弃: %q", got)
	}
}

func TestAutomaticRemoteAttachDoesNotInjectAtMenuWithoutConfirmation(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	path := filepath.Join(t.TempDir(), "mock-menu")
	script := "#!/bin/sh\nprintf 'WT_MENU>'\nIFS= read -r selection\nprintf 'WT_MENU_UNEXPECTED\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Write([]byte(f.s.quote(path) + "\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_MENU>")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := f.s.AttachAuto(ctx, "bash", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("未确认的接入应返回错误")
	}
	if strings.Contains(f.screen(), "WT_MENU_UNEXPECTED") {
		t.Fatal("用户未确认时向菜单发送了输入")
	}
}

func TestAutomaticRemoteAttachDeclined(t *testing.T) {
	f := remoteFixture(t)
	err := f.s.AttachAuto(context.Background(), "bash", func(context.Context) error {
		return errors.New("用户取消远端接入")
	})
	if err == nil || !strings.Contains(err.Error(), "取消") {
		t.Fatalf("取消确认应返回错误: %v", err)
	}
	if strings.Contains(f.screen(), "__wt_prompt_") {
		t.Fatal("取消确认后仍发送了接入命令")
	}
}

func TestAutomaticRemoteAttachRejectsNewOutput(t *testing.T) {
	f := remoteFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	prompted := make(chan struct{})
	confirm := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.s.AttachAuto(ctx, "bash", func(ctx context.Context) error {
			close(prompted)
			select {
			case <-confirm:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	<-prompted
	f.s.emit([]byte("background output"))
	close(confirm)
	if err := <-done; err == nil {
		t.Fatal("确认期间有新输出时不得自动接入")
	}
	if strings.Contains(f.screen(), "__wt_prompt_") {
		t.Fatal("终端输出变化后仍发送了接入命令")
	}
}

func TestAutomaticRemoteAttachRejectsInterveningInput(t *testing.T) {
	f := remoteFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	prompted := make(chan struct{})
	confirm := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.s.AttachAuto(ctx, "bash", func(ctx context.Context) error {
			close(prompted)
			select {
			case <-confirm:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	<-prompted
	if _, err := f.s.Write([]byte("printf user-input")); err != nil {
		t.Fatal(err)
	}
	close(confirm)
	if err := <-done; err == nil {
		t.Fatal("确认期间出现人工输入时不得自动接入")
	}
	if strings.Contains(f.screen(), "__wt_prompt_") {
		t.Fatal("人工输入后仍发送了接入命令")
	}
}

func TestAutomaticRemoteAttachCancelledWhileQueued(t *testing.T) {
	f := remoteFixture(t)
	queued := make(chan []byte, 1)
	f.s.SetSender(func(p []byte) { queued <- append([]byte(nil), p...) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- f.s.AttachAuto(ctx, "bash", func(context.Context) error { return nil })
	}()
	var pending []byte
	select {
	case pending = <-queued:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("未收到排队的接入请求")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("取消接入应返回错误")
	}
	if _, err := f.s.Write(pending); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.screen(), "__wt_prompt_") {
		t.Fatal("取消后仍发送了接入命令")
	}
}

func TestAutomaticRemoteAttachRejectsInputWhileQueued(t *testing.T) {
	f := remoteFixture(t)
	queued := make(chan []byte, 1)
	f.s.SetSender(func(p []byte) { queued <- append([]byte(nil), p...) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- f.s.AttachAuto(ctx, "bash", func(context.Context) error { return nil })
	}()
	var pending []byte
	select {
	case pending = <-queued:
	case <-ctx.Done():
		t.Fatal("未收到排队的接入请求")
	}
	if _, err := f.s.Write([]byte("printf user-input")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Write(pending); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("排队期间出现人工输入时不得自动接入")
	}
	if strings.Contains(f.screen(), "__wt_prompt_") {
		t.Fatal("排队期间出现输入后仍发送了接入命令")
	}
}

func TestAutomaticRemoteAttachRejectsKeyWhileQueued(t *testing.T) {
	f := remoteFixture(t)
	queued := make(chan []byte, 1)
	f.s.SetSender(func(p []byte) { queued <- append([]byte(nil), p...) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- f.s.AttachAuto(ctx, "bash", func(context.Context) error { return nil })
	}()
	var pending []byte
	select {
	case pending = <-queued:
	case <-ctx.Done():
		t.Fatal("未收到排队的接入请求")
	}
	if f.s.OnKey(0, ui.KeyEnter) {
		t.Fatal("未发送接入命令时不应阻止人工按键")
	}
	if _, err := f.s.Write(pending); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("排队期间出现人工按键时不得自动接入")
	}
}

func TestAutomaticRemoteAttachBlocksUntrackedInputDuringInjection(t *testing.T) {
	s := &Session{remote: &remoteSession{attaching: true, injected: true}}
	if !s.OnKey(0, ui.KeyA) || !s.OnPaste() {
		t.Fatal("自动输入接入命令时应暂停人工输入")
	}
	payload := []byte("untracked-input")
	if n, err := s.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("未跟踪的输入未被安全拦截: %d %v", n, err)
	}
	if !s.inputBlocked || len(s.remote.input) != 0 {
		t.Fatal("未跟踪的输入不应进入远端会话")
	}
}

func TestAutomaticRemoteAttachInterruptReportsUnknownResult(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	remote := &remoteSession{attaching: true, injected: true, attachDone: make(chan error, 1)}
	s := &Session{pty: writer, remote: remote}
	if s.OnKey(ui.Ctrl, ui.KeyC) {
		t.Fatal("自动接入期间不应拦截 Ctrl+C")
	}
	if _, err := s.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	var got [1]byte
	if _, err := reader.Read(got[:]); err != nil || got[0] != 3 {
		t.Fatalf("未向终端转发 Ctrl+C: %v %v", got, err)
	}
	select {
	case err := <-remote.attachDone:
		if err == nil || !strings.Contains(err.Error(), "可能已执行") {
			t.Fatalf("中断后应报告结果未知: %v", err)
		}
	default:
		t.Fatal("Ctrl+C 后未结束接入等待")
	}
}

func TestAutomaticApprovalCannotBypassHumanTakeover(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	if _, _, err := f.run(t, "printf ready"); err != nil {
		t.Fatal(err)
	}
	f.s.TakeOver()
	called := false
	err := f.s.AttachAuto(context.Background(), "bash", func(context.Context) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("人类接管期间仍执行自动审批: err=%v called=%v", err, called)
	}
}
