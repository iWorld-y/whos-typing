package shell

import (
	"context"
	"errors"
	"fmt"
	"os"

	"strings"
	"testing"
	"time"
	"whos-typing/internal/control"
)

func TestRemoteRecoveryAfterHumanInput(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		pending     int
	}{
		{"interactive", "read WT_REPLY\ranswer\r", 1},
		{"multiline", "{\rWT_REPLY=answer\r}\r", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := remoteFixture(t)
			attachFixture(t, f)
			if _, _, err := f.run(t, "printf ready"); err != nil {
				t.Fatal(err)
			}
			f.s.TakeOver()
			if _, err := f.s.Write([]byte(tc.input)); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				f.s.mu.Lock()
				pending := f.s.remote.pending
				oldToken := f.s.remote.token
				f.s.mu.Unlock()
				if pending == tc.pending {
					if err := f.s.ReturnControl(); !errors.Is(err, ErrRemoteNotReady) {
						t.Fatalf("应提供重新接入入口: %v", err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					if err := f.s.RecoverRemote(ctx, func(context.Context) error {
						if f.s.ControlOwner() != ControlHumanTakeover {
							t.Error("确认前已交还控制权")
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
					if f.s.ControlOwner() != ControlAgent {
						t.Fatal("握手成功后未交还 Agent")
					}
					out, code, err := f.run(t, `printf '%s\n' "$WT_REPLY" "$PROMPT_COMMAND" "$PS1"`)
					if err != nil || code != 0 || !strings.HasPrefix(strings.ReplaceAll(out, "\r", ""), "answer\n") || strings.Contains(out, oldToken) {
						t.Fatalf("重新接入未保留环境或未清理旧钩子: %q %d %v", out, code, err)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("未复现交互输入的残留计数")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestRemoteRecoveryCancelledOrChanged(t *testing.T) {
	for _, action := range []string{"cancel", "input", "output"} {
		t.Run(action, func(t *testing.T) {
			f := remoteFixture(t)
			attachFixture(t, f)
			if _, _, err := f.run(t, "printf ready"); err != nil {
				t.Fatal(err)
			}
			f.s.TakeOver()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := f.s.RecoverRemote(ctx, func(context.Context) error {
				switch action {
				case "cancel":
					return context.Canceled
				case "input":
					_, err := f.s.Write([]byte("draft"))
					return err
				case "output":
					f.s.emit([]byte("background output"))
				}
				return nil
			})
			if err == nil || f.s.ControlOwner() != ControlHumanTakeover {
				t.Fatalf("取消或状态改变后仍交还: %v", err)
			}
			f.s.mu.Lock()
			remote := f.s.remote != nil
			f.s.mu.Unlock()
			if !remote {
				t.Fatal("恢复失败后应保留再次恢复的远端目标")
			}
			if strings.Contains(f.screen(), "[CLI] 正在接入远端 Bash") {
				t.Fatal("取消或状态改变后仍注入了接入命令")
			}
		})
	}
}

func TestRemoteRecoveryRejectsQueuedHumanInput(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	if _, _, err := f.run(t, "printf ready"); err != nil {
		t.Fatal(err)
	}
	f.s.TakeOver()
	queued := make(chan []byte, 1)
	f.s.SetSender(func(p []byte) { queued <- append([]byte(nil), p...) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.s.RecoverRemote(ctx, func(context.Context) error { return nil }) }()
	var barrier []byte
	select {
	case barrier = <-queued:
	case <-ctx.Done():
		t.Fatal("未收到恢复屏障")
	}
	if f.s.ControlOwner() != ControlHumanTakeover || f.s.Execute(ctx, "printf wrong").Err == nil {
		t.Fatal("握手前不能交还或接受 Agent 命令")
	}
	if _, err := f.s.Write([]byte("draft")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Write(barrier); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || f.s.ControlOwner() != ControlHumanTakeover {
		t.Fatalf("排队期间有人工输入仍执行了恢复: %v", err)
	}
	waitText(t, f, "draft")
	if strings.Contains(f.screen(), "[CLI] 正在接入远端 Bash") {
		t.Fatal("恢复覆盖了未提交输入")
	}
}

func TestRemoteRecoveryPreservesPromptArray(t *testing.T) {
	f := remoteFixture(t)
	if _, err := f.s.Write([]byte("PROMPT_COMMAND=('printf ORIGINAL_ONE' 'printf ORIGINAL_TWO')\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "ORIGINAL_ONE")
	attachFixture(t, f)
	for range 2 {
		if _, _, err := f.run(t, "printf ready"); err != nil {
			t.Fatal(err)
		}
		f.s.TakeOver()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := f.s.RecoverRemote(ctx, func(context.Context) error { return nil })
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		out, code, err := f.run(t, `declare -p PROMPT_COMMAND`)
		if err != nil || code != 0 || !strings.Contains(out, "ORIGINAL_ONE") || !strings.Contains(out, "ORIGINAL_TWO") || strings.Count(out, "__wt_prompt_") != 1 {
			t.Fatalf("重复恢复修改了用户钩子或叠加了接入钩子: %q %d %v", out, code, err)
		}
	}
}

func TestRemoteRecoveryStateValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*Session, *remoteSession)
		allowed   bool
	}{
		{"dirty", func(_ *Session, r *remoteSession) { r.dirty = true }, false},
		{"paste", func(_ *Session, r *remoteSession) { r.pasting = true }, false},
		{"injecting", func(s *Session, r *remoteSession) {
			s.active = &request{remote: true, remoteBinding: r, injecting: true}
		}, false},
		{"active", func(s *Session, r *remoteSession) { s.active = &request{remote: true, remoteBinding: r} }, true},
		{"queued", func(s *Session, r *remoteSession) { s.queued = &request{remote: true, remoteBinding: r} }, true},
		{"orphan", func(_ *Session, r *remoteSession) { r.orphan = &request{remote: true, remoteBinding: r} }, true},
		{"unknown", func(s *Session, r *remoteSession) {
			s.unconfirmed = map[*request]struct{}{&request{remote: true, remoteBinding: r}: {}}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &remoteSession{}
			s := &Session{owner: ControlHumanTakeover, remote: r}
			tc.configure(s, r)
			err := s.canRecoverRemote()
			if (err == nil) != tc.allowed {
				t.Fatalf("允许=%v, 错误=%v", tc.allowed, err)
			}
		})
	}
}

func TestRemoteRecoverUnknownNestedBash(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint("timeout=", timeout), func(t *testing.T) {
			f := remoteFixture(t)
			if _, err := f.s.Write([]byte("PS1='WT_RECOVER_INNER> ' /bin/bash --noprofile --norc -i\r")); err != nil {
				t.Fatal(err)
			}
			waitText(t, f, "WT_RECOVER_INNER>")
			attachFixture(t, f)
			if _, code, err := f.run(t, "trap - EXIT; set -e"); err != nil || code != 0 {
				t.Fatalf("准备失败: %d %v", code, err)
			}
			duration := 10 * time.Second
			if timeout {
				duration = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()
			done := make(chan control.Result, 1)
			go func() { done <- f.s.Execute(ctx, "false") }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				f.s.mu.Lock()
				active := f.s.active != nil && f.s.active.started
				f.s.mu.Unlock()
				if active {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("命令未开始")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if timeout {
				result := <-done
				if result.Err == nil || !strings.Contains(result.Err.Error(), "结果未知") {
					t.Fatalf("超时结果: %v", result.Err)
				}
			}
			f.s.TakeOver()
			recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer recoverCancel()
			if err := f.s.RecoverRemote(recoverCtx, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if !timeout {
				result := <-done
				if result.Output != nil {
					defer result.Output.Close()
					defer os.Remove(result.Output.Name())
				}
				if result.Err == nil || !strings.Contains(result.Err.Error(), "结果未知") {
					t.Fatalf("旧等待结果: %v", result.Err)
				}
			}
			if output, code, err := f.run(t, "printf recovered"); err != nil || code != 0 || output != "recovered" {
				t.Fatalf("恢复失败: %q %d %v", output, code, err)
			}
		})
	}
}

func TestRemoteRecoveryRejectsInputBeingInjected(t *testing.T) {
	for _, active := range []bool{true, false} {
		r := &request{remote: true, injecting: true}
		old := &remoteSession{}
		s := &Session{owner: ControlHumanTakeover, remote: old}
		if active {
			s.active = r
		} else {
			old.orphan = r
		}
		if err := s.canRecoverRemote(); err == nil {
			t.Fatal("发送进行中仍允许恢复")
		}
		r.injecting = false
		if err := s.canRecoverRemote(); err != nil {
			t.Fatalf("发送结束后应允许确认: %v", err)
		}
	}
}

func TestRemoteRecoveryLateMarkerCannotAffectNewRequest(t *testing.T) {
	old := &remoteSession{token: "old"}
	current := &remoteSession{token: "new", ready: true}
	s := &Session{remote: current, owner: ControlAgent}
	if !s.controlRemote(old, "X:123:1") || !s.controlRemote(old, "P:1") || !s.controlRemote(old, "E:0") {
		t.Fatal("旧标记未被消费")
	}
	if s.remote != current || !current.ready || s.owner != ControlAgent {
		t.Fatal("旧标记改变了新接入")
	}
}

func TestRemoteRecoveryRequestCancelledDuringConfirmation(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint("timeout=", timeout), func(t *testing.T) { testRequestEndsDuringConfirmation(t, timeout) })
	}
}

func testRequestEndsDuringConfirmation(t *testing.T, timeout bool) {
	t.Helper()
	f := remoteFixture(t)
	if _, err := f.s.Write([]byte("PS1='WT_CANCEL_INNER> ' /bin/bash --noprofile --norc -i\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_CANCEL_INNER>")
	attachFixture(t, f)
	if _, _, err := f.run(t, "trap - EXIT; set -e"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if timeout {
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), 700*time.Millisecond)
	}
	defer cancel()
	done := make(chan control.Result, 1)
	go func() { done <- f.s.Execute(ctx, "false") }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.s.mu.Lock()
		started := f.s.active != nil && f.s.active.started
		f.s.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("命令未开始")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.s.TakeOver()
	recoverCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	err := f.s.RecoverRemote(recoverCtx, func(context.Context) error {
		if !timeout {
			cancel()
		}
		result := <-done
		if result.Err == nil || !strings.Contains(result.Err.Error(), "结果未知") {
			t.Errorf("取消结果: %v", result.Err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("确认期间取消旧请求后恢复失败: %v", err)
	}
	if output, code, err := f.run(t, "printf recovered"); err != nil || code != 0 || output != "recovered" {
		t.Fatalf("恢复后不能执行: %q %d %v", output, code, err)
	}
}

func TestCancelledCompletedRequestDoesNotChangeNewRemote(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan control.Result, 1)
	go func() { done <- f.s.Execute(ctx, "sleep 1") }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.s.mu.Lock()
		active := f.s.active
		if active != nil && active.started {
			old := f.s.remote
			fresh := &remoteSession{ready: true}
			cancel()
			f.s.abandonRemoteRequests(old)
			f.s.remote = fresh
			f.s.mu.Unlock()
			result := <-done
			if result.Output != nil {
				defer os.Remove(result.Output.Name())
				defer result.Output.Close()
			}
			if result.Err == nil || !strings.Contains(result.Err.Error(), "结果未知") {
				t.Fatalf("请求完成结果: %v", result.Err)
			}
			f.s.mu.Lock()
			ready, orphan := fresh.ready, fresh.orphan
			f.s.mu.Unlock()
			if !ready || orphan != nil {
				t.Fatal("完成与取消竞争污染新接入")
			}
			return
		}
		f.s.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("命令未开始")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRemoteRecoveryRetriesAfterHandshakeFailure(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	if _, _, err := f.run(t, "shopt -u promptvars"); err != nil {
		t.Fatal(err)
	}
	f.s.TakeOver()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.s.RecoverRemote(ctx, func(context.Context) error { return nil }); err == nil {
		t.Fatal("无效握手应失败")
	}
	if f.s.ControlOwner() != ControlHumanTakeover {
		t.Fatal("失败后应保留人类控制")
	}
	if _, err := f.s.Write([]byte("shopt -s promptvars; printf WT_RETRY_%s READY\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_RETRY_READY")
	deadline := time.Now().Add(3 * time.Second)
	for {
		screen := f.screen()
		pos := strings.LastIndex(screen, "WT_RETRY_READY")
		if pos >= 0 && strings.Contains(screen[pos:], "WT_REMOTE_PROMPT>") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待修复后的空提示符超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if err := f.s.RecoverRemote(ctx2, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("修复目标后无法重试: %v，终端: %q", err, f.screen())
	}
	if output, code, err := f.run(t, "printf retried"); err != nil || code != 0 || output != "retried" {
		t.Fatalf("重试后执行: %q %d %v", output, code, err)
	}
}
