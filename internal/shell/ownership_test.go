package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"whos-typing/internal/control"
)

func TestControlOwnerPersistsBetweenCommands(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	if got := f.s.ControlOwner(); got != ControlHuman {
		t.Fatalf("初始控制方错误: %v", got)
	}
	if output, code, err := f.run(t, "printf first"); err != nil || code != 0 || output != "first" {
		t.Fatalf("CLI 命令失败: %q %d %v", output, code, err)
	}
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("命令结束后 Agent 应保留控制权: %v", got)
	}
	if !f.s.OnKey(0, ui.KeyA) || !f.s.OnPaste() {
		t.Fatal("Agent 控制期间不应接收普通人工输入")
	}
	if f.s.OnKey(ui.Ctrl, ui.KeyC) {
		t.Fatal("Agent 控制期间应保留 Ctrl+C")
	}

	f.s.TakeOver()
	if got := f.s.ControlOwner(); got != ControlHumanTakeover {
		t.Fatalf("用户接管后控制方错误: %v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := f.s.Execute(ctx, "printf blocked")
	if result.Err == nil || !strings.Contains(result.Err.Error(), "人类") {
		t.Fatalf("人类接管期间应拒绝 CLI 命令: %+v", result)
	}
	if got := f.s.ControlOwner(); got != ControlHumanTakeover {
		t.Fatalf("被拒绝的 CLI 请求不能解除人类接管: %v", got)
	}
}

func TestTakeOverCancelsQueuedCommand(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	queued := make(chan []byte, 1)
	f.s.SetSender(func(p []byte) { queued <- append([]byte(nil), p...) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan control.Result, 1)
	go func() { done <- f.s.Execute(ctx, "printf SHOULD_NOT_RUN") }()
	var barrier []byte
	select {
	case barrier = <-queued:
	case <-ctx.Done():
		t.Fatal("未收到排队命令")
	}
	f.s.TakeOver()
	if _, err := f.s.Write(barrier); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Output != nil {
			_ = os.Remove(result.Output.Name())
			_ = result.Output.Close()
		}
		if result.Err == nil || !strings.Contains(result.Err.Error(), "接管") {
			t.Fatalf("排队命令应报告接管: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("接管后排队命令未返回")
	}
	if strings.Contains(f.screen(), "SHOULD_NOT_RUN") {
		t.Fatal("接管后发送了排队命令")
	}
}

func TestTakeOverCancelsQueuedRemoteInjection(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	queued := make(chan []byte, 2)
	f.s.SetSender(func(p []byte) { queued <- append([]byte(nil), p...) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan control.Result, 1)
	go func() { done <- f.s.Execute(ctx, "printf SHOULD_NOT_RUN") }()
	var barrier []byte
	select {
	case barrier = <-queued:
	case <-ctx.Done():
		t.Fatal("未收到远端命令屏障")
	}
	if _, err := f.s.Write(barrier); err != nil {
		t.Fatal(err)
	}
	var injection []byte
	select {
	case injection = <-queued:
	case <-ctx.Done():
		t.Fatal("未收到远端输入队列")
	}
	f.s.TakeOver()
	if _, err := f.s.Write(injection); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Output != nil {
			_ = os.Remove(result.Output.Name())
			_ = result.Output.Close()
		}
		if result.Err == nil || !strings.Contains(result.Err.Error(), "接管") {
			t.Fatalf("未发送的远端命令应报告接管: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("接管后远端排队命令未返回")
	}
	if strings.Contains(f.screen(), "SHOULD_NOT_RUN") {
		t.Fatal("接管后发送了远端命令")
	}
}

func TestTakeOverCancelsBlockedRemoteWrite(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	output, err := os.CreateTemp(t.TempDir(), "output-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	r := &request{remote: true, output: output, done: make(chan struct{})}
	s := &Session{pty: writer, token: "test", owner: ControlAgent, remote: &remoteSession{}, active: r}
	r.remoteBinding = s.remote
	prefix := "\x00WT-INJECT:test:" + filepath.Base(output.Name()) + ":"
	r.inject = []byte(prefix + strings.Repeat("x", 1<<20))
	done := make(chan error, 1)
	go func() {
		_, err := s.Write(r.inject)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		injecting := r.injecting
		s.mu.Unlock()
		if injecting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.mu.Lock()
	injecting := r.injecting
	s.mu.Unlock()
	if !injecting {
		t.Fatal("远端写入未进入阻塞状态")
	}
	s.TakeOver()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("接管后阻塞的 Agent 写入未停止")
	}
	select {
	case <-r.done:
		if r.result.Err == nil || !strings.Contains(r.result.Err.Error(), "结果未知") {
			t.Fatalf("部分发送应报告结果未知: %+v", r.result)
		}
	case <-time.After(time.Second):
		t.Fatal("接管后 CLI 请求未返回")
	}
	buffer := make([]byte, 64<<10)
	n, err := reader.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("H")); err != nil {
		t.Fatalf("接管后人工输入失败: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	received := append(buffer[:n], rest...)
	if len(received) >= 1<<20 || !strings.HasSuffix(string(received), "H") {
		t.Fatal("接管后 Agent 写入未停止，或人工输入未到达终端")
	}
}

func TestTakeOverRunningCommand(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			var f *fixture
			if remote {
				f = remoteFixture(t)
				attachFixture(t, f)
			} else {
				f = newFixture(t, "/bin/zsh")
			}
			done := make(chan control.Result, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			go func() {
				done <- f.s.Execute(ctx, "printf 'WT_WAIT_INPUT\\n'; IFS= read -r value; printf 'WT_VALUE:%s\\n' \"$value\"")
			}()
			waitText(t, f, "WT_WAIT_INPUT")
			f.s.TakeOver()
			if err := f.s.ReturnControl(); err == nil {
				t.Fatal("前台命令仍在运行时不应交还控制权")
			}
			if f.s.OnKey(0, ui.KeyA) || f.s.OnKey(0, ui.KeyEnter) {
				t.Fatal("接管后人工输入仍被阻止")
			}
			if _, err := f.s.Write([]byte("hello\r")); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if result.Output == nil {
					t.Fatalf("命令未返回输出: %v", result.Err)
				}
				data, err := io.ReadAll(result.Output)
				_ = os.Remove(result.Output.Name())
				_ = result.Output.Close()
				if err != nil || result.Err != nil || result.ExitCode != 0 || !strings.Contains(string(data), "WT_VALUE:hello") {
					t.Fatalf("接管后命令未正常继续: %q %d %v %v", data, result.ExitCode, result.Err, err)
				}
			case <-ctx.Done():
				t.Fatal("接管后正在运行的命令未完成")
			}
			if remote {
				waitRemoteReady(t, f)
			} else {
				f.waitReady(t)
			}
		})
	}
}

func TestReturnControlRequiresReadyTerminal(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	if _, _, err := f.run(t, "printf ready"); err != nil {
		t.Fatal(err)
	}
	f.s.TakeOver()
	if f.s.OnKey(0, ui.KeyA) {
		t.Fatal("人类接管后普通输入被阻止")
	}
	if _, err := f.s.Write([]byte("printf unfinished")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "unfinished")
	if err := f.s.ReturnControl(); err == nil {
		t.Fatal("存在未提交输入时不应交还控制权")
	}
	if got := f.s.ControlOwner(); got != ControlHumanTakeover {
		t.Fatalf("未完成输入被交还后控制方错误: %v", got)
	}
	if f.s.OnKey(ui.Ctrl, ui.KeyC) {
		t.Fatal("人类接管后 Ctrl+C 被阻止")
	}
	if _, err := f.s.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	f.waitReady(t)
	if err := f.s.ReturnControl(); err != nil {
		t.Fatalf("终端就绪后无法交还: %v", err)
	}
	if got := f.s.ControlOwner(); got != ControlAgent {
		t.Fatalf("交还后控制方错误: %v", got)
	}
	if output, code, err := f.run(t, "printf resumed"); err != nil || code != 0 || output != "resumed" {
		t.Fatalf("交还后 CLI 无法执行: %q %d %v", output, code, err)
	}
}

func TestReturnControlAfterHumanForegroundShell(t *testing.T) {
	for _, manualTakeover := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual_takeover_%v", manualTakeover), func(t *testing.T) {
			f := newFixture(t, "/bin/zsh")
			if manualTakeover {
				if _, _, err := f.run(t, "printf ready"); err != nil {
					t.Fatal(err)
				}
				f.s.TakeOver()
			}
			if _, err := f.s.Write([]byte("/bin/bash --noprofile --norc -i\r")); err != nil {
				t.Fatal(err)
			}
			waitForegroundGroup(t, f.s)
			if f.s.OnKey(0, ui.KeyP) || f.s.OnKey(0, ui.KeyEnter) {
				t.Fatal("前台子 Shell 的人工输入被阻止")
			}
			if _, err := f.s.Write([]byte("printf child\\n\rexit\r")); err != nil {
				t.Fatal(err)
			}
			f.waitReady(t)
			if manualTakeover {
				if err := f.s.ReturnControl(); err != nil {
					t.Fatalf("退出前台子 Shell 后无法交还: %v", err)
				}
			}
			if output, _, err := f.run(t, "printf resumed"); err != nil || output != "resumed" {
				t.Fatalf("退出子 Shell 后 CLI 无法执行: %q %v", output, err)
			}
		})
	}
}

func TestReleaseControlErrorMatchesLocalAndRemoteState(t *testing.T) {
	local := &Session{owner: ControlAgent, active: &request{}}
	if err := local.ReleaseControl(); err == nil || strings.Contains(err.Error(), "远端") {
		t.Fatalf("本地命令错误提示: %v", err)
	}
	remote := &Session{owner: ControlAgent, remote: &remoteSession{orphan: &request{}}}
	if err := remote.ReleaseControl(); err == nil || !strings.Contains(err.Error(), "恢复远端接入") {
		t.Fatalf("远端未知提示: %v", err)
	}
}

func TestRemoteDiagnosticAndReleaseStates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(*Session, *remoteSession)
		unknown bool
		action  string
		release string
	}{
		{"active", func(s *Session, r *remoteSession) { s.active = &request{remote: true, remoteBinding: r} }, false, "等待", "等待"},
		{"finished_orphan", func(s *Session, r *remoteSession) {
			r.orphan = &request{remote: true, remoteBinding: r, finished: true}
		}, false, "恢复远端接入", "恢复远端接入"},
		{"unknown", func(s *Session, r *remoteSession) { r.orphan = &request{remote: true, remoteBinding: r} }, true, "恢复远端接入", "恢复远端接入"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &remoteSession{}
			s := &Session{owner: ControlAgent, remote: r}
			tc.setup(s, r)
			got := s.SessionDiagnostic()
			if got.Unknown != tc.unknown || !strings.Contains(got.SuggestedAction, tc.action) {
				t.Fatalf("诊断: %+v", got)
			}
			if err := s.ReleaseControl(); err == nil || !strings.Contains(err.Error(), tc.release) {
				t.Fatalf("交还提示: %v", err)
			}
		})
	}
}
