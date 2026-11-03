package shell

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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

func TestRemoteRecoveryRejectsUnfinishedState(t *testing.T) {
	for _, state := range []string{"dirty", "active", "queued", "orphan", "unknown", "paste"} {
		t.Run(state, func(t *testing.T) {
			r := &remoteSession{}
			s := &Session{owner: ControlHumanTakeover, remote: r, send: func([]byte) { t.Error("不应发送输入") }}
			switch state {
			case "dirty":
				r.dirty = true
			case "active":
				s.active = &request{}
			case "queued":
				s.queued = &request{}
			case "orphan":
				r.orphan = &request{}
			case "unknown":
				s.unconfirmed = map[*request]struct{}{&request{}: {}}
			case "paste":
				r.pasting = true
			}
			if err := s.RecoverRemote(context.Background(), func(context.Context) error {
				t.Error("未完成状态不应进入确认")
				return nil
			}); err == nil {
				t.Fatal("未完成状态被重新接入")
			}
		})
	}
}
