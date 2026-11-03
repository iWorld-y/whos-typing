// 本文件验证人工输入与排队中的 CLI 命令之间的先后顺序。
package shell

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"whos-typing/internal/control"
)

func queueTestCommand(t *testing.T, f *fixture, ctx context.Context) ([]byte, <-chan control.Result) {
	t.Helper()
	queued := make(chan []byte, 1)
	f.s.SetSender(func(p []byte) { queued <- append([]byte(nil), p...) })
	done := make(chan control.Result, 1)
	go func() { done <- f.s.Execute(ctx, "printf WT_AGENT_COMMAND") }()
	select {
	case barrier := <-queued:
		return barrier, done
	case <-ctx.Done():
		t.Fatal("未收到排队命令")
		return nil, nil
	}
}

func finishTestResult(t *testing.T, done <-chan control.Result) control.Result {
	t.Helper()
	select {
	case result := <-done:
		if result.Output != nil {
			_ = os.Remove(result.Output.Name())
			_ = result.Output.Close()
		}
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("排队命令未返回")
		return control.Result{}
	}
}

func TestQueuedHumanInputPrecedesCLICommand(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "remote"}[remote], func(t *testing.T) {
			var f *fixture
			if remote {
				f = remoteFixture(t)
				attachFixture(t, f)
				waitRemoteReady(t, f)
			} else {
				f = newFixture(t, "/bin/zsh")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			barrier, done := queueTestCommand(t, f, ctx)
			if _, err := f.s.Write([]byte("printf 'WT_HUMAN_%s' 'INPUT'")); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Write(barrier); err != nil {
				t.Fatal(err)
			}
			if result := finishTestResult(t, done); result.Err == nil {
				t.Fatal("先于命令排队的人工输入应阻止 CLI 执行")
			}
			if f.s.ControlOwner() != ControlHuman || f.s.OnKey(0, ui.KeyEnter) {
				t.Fatal("命令被拒绝后人类应保留输入控制权")
			}
			if _, err := f.s.Write([]byte("\r")); err != nil {
				t.Fatal(err)
			}
			waitText(t, f, "WT_HUMAN_INPUT")
			if strings.Contains(f.screen(), "WT_AGENT_COMMAND") {
				t.Fatal("CLI 命令不应执行")
			}
			if remote {
				waitRemoteReady(t, f)
			} else {
				f.waitReady(t)
			}
			f.s.SetSender(func(p []byte) { f.input <- append([]byte(nil), p...) })
			if output, code, err := f.run(t, "printf resumed"); err != nil || code != 0 || output != "resumed" {
				t.Fatalf("人工输入后无法继续使用 CLI: %q %d %v", output, code, err)
			}
		})
	}
}

func TestQueuedInputRemainsBlockedWhenAgentAlreadyControls(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	if _, _, err := f.run(t, "printf ready"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	barrier, done := queueTestCommand(t, f, ctx)
	if _, err := f.s.Write([]byte("WT_UNWANTED_HUMAN_INPUT")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Write(barrier); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Output == nil {
		t.Fatalf("CLI 命令未返回输出: %v", result.Err)
	}
	data, err := io.ReadAll(result.Output)
	_ = os.Remove(result.Output.Name())
	_ = result.Output.Close()
	if err != nil || result.Err != nil || string(data) != "WT_AGENT_COMMAND" {
		t.Fatalf("Agent 控制期间命令执行失败: %q %v %v", data, result.Err, err)
	}
	if strings.Contains(f.screen(), "WT_UNWANTED_HUMAN_INPUT") {
		t.Fatal("Agent 已持有控制权时不应接收人工输入")
	}
}

func TestCancelledQueuedCommandRestoresHumanControl(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	barrier, done := queueTestCommand(t, f, ctx)
	cancel()
	if result := finishTestResult(t, done); result.Err == nil {
		t.Fatal("取消排队命令应返回错误")
	}
	if f.s.ControlOwner() != ControlHuman {
		t.Fatal("排队命令取消后应恢复人类控制权")
	}
	if _, err := f.s.Write([]byte("printf 'WT_CANCEL_%s' 'INPUT'\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Write(barrier); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_CANCEL_INPUT")
	if strings.Contains(f.screen(), "WT_AGENT_COMMAND") {
		t.Fatal("取消后不应执行 CLI 命令")
	}
}
