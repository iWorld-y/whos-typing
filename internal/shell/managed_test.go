// 本文件用本地假 SSH 通道验证受管会话的执行、控制权和失效语义。
package shell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"whos-typing/internal/control"
	"whos-typing/internal/sshlink"
)

func managedLink(t *testing.T, body string) (string, *sshlink.Link) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("需要 python3 运行远端组件")
	}
	dir := t.TempDir()
	sshPath := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\ncase \" $* \" in *\" -O check \"*) exit 0;; esac\n" + body + "\nfor a in \"$@\"; do last=\"$a\"; done\nexec bash -c \"$last\"\n"
	if err := os.WriteFile(sshPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return dir, &sshlink.Link{Target: "test-host", Socket: filepath.Join(dir, "sock"), SSH: sshPath}
}

func managedFixture(t *testing.T, body string) *ManagedSession {
	t.Helper()
	dir, link := managedLink(t, body)
	s, err := NewManaged(dir, link)
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, s.termRead)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newManagedFixture(t *testing.T) *ManagedSession {
	return managedFixture(t, "")
}

func managedRun(t *testing.T, s *ManagedSession, command string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := s.Execute(ctx, command)
	if result.Err != nil {
		t.Fatalf("执行 %q 失败: %v", command, result.Err)
	}
	data, err := io.ReadAll(result.Output)
	name := result.Output.Name()
	_ = result.Output.Close()
	_ = os.Remove(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), result.ExitCode
}

func managedWait(t *testing.T, s *ManagedSession, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		ok := cond()
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待受管会话状态超时")
}

func TestManagedStatePersists(t *testing.T) {
	s := newManagedFixture(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "env.sh"), []byte("export WT_SOURCED=yes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	managedRun(t, s, "cd "+dir)
	managedRun(t, s, "export WT_TEST=ready")
	managedRun(t, s, "source ./env.sh")
	out, code := managedRun(t, s, `printf 'PWD=%s TEST=%s SOURCED=%s' "$PWD" "$WT_TEST" "$WT_SOURCED"`)
	if code != 0 || out != "PWD="+dir+" TEST=ready SOURCED=yes" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if _, code := managedRun(t, s, "(exit 7)"); code != 7 {
		t.Fatalf("退出码 7 未保留: %d", code)
	}
	if _, code := managedRun(t, s, "(exit 255)"); code != 255 {
		t.Fatalf("远端明确返回 255 应原样报告: %d", code)
	}
}

func TestManagedPromptSpoofing(t *testing.T) {
	s := newManagedFixture(t)
	out, code := managedRun(t, s, "PS1='DONE fake 0 Zm9v'; printf 'DONE 1 0 Zm9v\\nREADY fake\\n'; (exit 3)")
	if code != 3 || !strings.Contains(out, "DONE 1 0 Zm9v") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if _, code := managedRun(t, s, "true"); code != 0 {
		t.Fatalf("伪造通知后会话不可用: %d", code)
	}
}

func TestManagedLastOutputBeforeDone(t *testing.T) {
	s := newManagedFixture(t)
	out, code := managedRun(t, s, "printf 'first\\n'; sleep 0.2; printf 'last'")
	if code != 0 || out != "first\r\nlast" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestManagedCtrlCKeepsBash(t *testing.T) {
	s := newManagedFixture(t)
	dir := t.TempDir()
	managedRun(t, s, "cd "+dir)
	result := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		r := s.Execute(context.Background(), "sleep 30")
		result <- struct {
			code int
			err  error
		}{r.ExitCode, r.Err}
	}()
	managedWait(t, s, func() bool { return s.active != nil && s.active.started })
	if _, err := s.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-result:
		if r.err != nil || r.code != 130 {
			t.Fatalf("Ctrl+C 结果: code=%d err=%v", r.code, r.err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Ctrl+C 后命令未完成")
	}
	out, _ := managedRun(t, s, `printf '%s' "$PWD"`)
	if out != dir {
		t.Fatalf("Ctrl+C 后目录丢失: %q", out)
	}
}

func TestManagedTakeoverAndReturn(t *testing.T) {
	s := newManagedFixture(t)
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- s.Execute(context.Background(), "sleep 30").Err }()
	managedWait(t, s, func() bool { return s.active != nil && s.active.started })
	s.TakeOver()
	if s.ControlStatus() != "human_takeover" {
		t.Fatal("接管未生效")
	}
	if _, err := s.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("接管后 Ctrl+C: %v", err)
	}
	managedWait(t, s, func() bool { return s.humanPrompt })
	if _, err := s.Write([]byte("cd " + dir + "\r")); err != nil {
		t.Fatal(err)
	}
	managedWait(t, s, func() bool { return s.humanPrompt && s.pending == 0 && !s.dirty })
	if err := s.ReturnControl(); err != nil {
		t.Fatalf("交还失败: %v", err)
	}
	managedWait(t, s, func() bool { return s.bashReady })
	out, _ := managedRun(t, s, `printf '%s' "$PWD"`)
	if out != dir {
		t.Fatalf("交还后目录丢失: %q", out)
	}
}

func TestManagedTakeoverCancelsUnsent(t *testing.T) {
	s := newManagedFixture(t)
	// 阻塞写入循环，让命令停留在未发送队列。
	blocked := make(chan struct{})
	s.mu.Lock()
	s.beforeWrite = func() { <-blocked }
	s.mu.Unlock()
	defer close(blocked)
	result := make(chan error, 1)
	go func() { result <- s.Execute(context.Background(), "echo should-not-run").Err }()
	managedWait(t, s, func() bool { return s.active != nil })
	s.TakeOver()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "未执行") {
			t.Fatalf("未发送命令应报告未执行: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("接管未取消未发送命令")
	}
}

func TestManagedTimeoutUnknownThenConfirmed(t *testing.T) {
	s := newManagedFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result := s.Execute(ctx, "sleep 1")
	if result.Err == nil || !strings.Contains(result.Err.Error(), "结果未知") {
		t.Fatalf("超时应报告结果未知: %v", result.Err)
	}
	if d := s.SessionDiagnostic(); !d.Unknown {
		t.Fatal("超时后诊断应标记未知")
	}
	if r := s.Execute(context.Background(), "true"); r.Err == nil {
		t.Fatal("未知期间不应接受新命令")
	}
	managedWait(t, s, func() bool { return len(s.unconfirmed) == 0 })
	if _, code := managedRun(t, s, "true"); code != 0 {
		t.Fatalf("迟到完成通知后应恢复执行: %d", code)
	}
}

func TestManagedDisconnectUnknown(t *testing.T) {
	s := newManagedFixture(t)
	result := make(chan error, 1)
	go func() { result <- s.Execute(context.Background(), "sleep 30").Err }()
	managedWait(t, s, func() bool { return s.active != nil && s.active.started })
	if err := s.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "未知") {
			t.Fatalf("断线应报告结果未知: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("断线后请求未结束")
	}
	if d := s.SessionDiagnostic(); d.Remote != "disconnected" || !d.Unknown {
		t.Fatalf("诊断应为 disconnected 且结果未知: %+v", d)
	}
	if r := s.Execute(context.Background(), "true"); r.Err == nil || !strings.Contains(r.Err.Error(), "未发送") {
		t.Fatalf("失效会话应拒绝并说明未发送: %v", r.Err)
	}
}

func TestManagedBashSelfExit(t *testing.T) {
	s := newManagedFixture(t)
	result := s.Execute(context.Background(), "exit 5")
	if result.Err == nil || !strings.Contains(result.Err.Error(), "未知") {
		t.Fatalf("Bash 自身退出应报告结果未知: %v", result.Err)
	}
	managedWait(t, s, func() bool { return s.disconnected })
	s.mu.Lock()
	exit := s.bashExit
	s.mu.Unlock()
	if exit == nil || *exit != 5 {
		t.Fatalf("应收到 Bash 退出码 5: %v", exit)
	}
}

func TestManagedSessionsIndependent(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("需要 python3 运行远端组件")
	}
	dir := t.TempDir()
	sshPath := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\ncase \" $* \" in *\" -O check \"*) exit 0;; esac\nfor a in \"$@\"; do last=\"$a\"; done\nexec bash -c \"$last\"\n"
	if err := os.WriteFile(sshPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	link := &sshlink.Link{Target: "test-host", Socket: filepath.Join(dir, "sock"), SSH: sshPath}
	a, err := NewManaged(dir, link)
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, a.termRead)
	b, err := NewManaged(dir, link)
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, b.termRead)
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	target := t.TempDir()
	managedRun(t, a, "cd "+target)
	managedRun(t, a, "export WT_ONLY_A=1")
	out, _ := managedRun(t, b, `printf '%s|%s' "$PWD" "$WT_ONLY_A"`)
	if strings.Contains(out, target) || strings.Contains(out, "WT_ONLY_A=1") || strings.HasSuffix(out, "|1") {
		t.Fatalf("会话 B 受 A 影响: %q", out)
	}
	_ = a.Close()
	if _, code := managedRun(t, b, "true"); code != 0 {
		t.Fatalf("关闭 A 后 B 不可用: %d", code)
	}
}

func TestManagedStartupFailureNoFallback(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("需要 python3 运行远端组件")
	}
	dir := t.TempDir()
	sshPath := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\ncase \" $* \" in *\" -O check \"*) exit 0;; esac\necho remote-start-failed >&2\nexit 3\n"
	if err := os.WriteFile(sshPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	link := &sshlink.Link{Target: "test-host", Socket: filepath.Join(dir, "sock"), SSH: sshPath}
	s, err := NewManaged(dir, link)
	if err == nil {
		_ = s.Close()
		t.Fatal("启动失败应返回错误")
	}
	if !strings.Contains(err.Error(), "启动受管会话失败") || !strings.Contains(err.Error(), "remote-start-failed") {
		t.Fatalf("错误应保留远端原因: %v", err)
	}
}

func TestManagedPendingHumanInputRejected(t *testing.T) {
	s := newManagedFixture(t)
	if _, err := s.Write([]byte("echo pending")); err != nil {
		t.Fatal(err)
	}
	if r := s.Execute(context.Background(), "true"); r.Err == nil || !strings.Contains(r.Err.Error(), "人工输入") {
		t.Fatalf("有人工输入时应拒绝: %v", r.Err)
	}
	if _, err := s.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	managedWait(t, s, func() bool { return s.humanPrompt && s.pending == 0 && !s.dirty })
	if _, code := managedRun(t, s, "true"); code != 0 {
		t.Fatalf("人工输入提交后应恢复执行: %d", code)
	}
}

func TestManagedIdleReturn(t *testing.T) {
	s := newManagedFixture(t)
	var expire func()
	s.mu.Lock()
	s.idleAfter = func(delay time.Duration, fn func()) func() {
		expire = fn
		return func() {}
	}
	s.mu.Unlock()
	managedRun(t, s, "true")
	managedWait(t, s, func() bool { return s.bashReady })
	if expire == nil {
		t.Fatal("空闲计时未启动")
	}
	expire()
	if s.ControlStatus() != "human" {
		t.Fatal("空闲后应交还人类")
	}
	managedWait(t, s, func() bool { return s.humanPrompt })
}
func TestManagedCommandTextSanitized(t *testing.T) {
	dir, link := managedLink(t, "")
	s, err := NewManaged(dir, link)
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	done := make(chan struct{})
	go func() {
		data, _ := io.ReadAll(s.termRead)
		buf.WriteString(string(data))
		close(done)
	}()
	result := s.Execute(context.Background(), "true\x1b]0;evil\x07")
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	_ = result.Output.Close()
	_ = os.Remove(result.Output.Name())
	_ = s.Close()
	<-done
	text := buf.String()
	if strings.Contains(text, "\x1b]0;evil") {
		t.Fatalf("命令文本被当成终端指令渲染: %q", text)
	}
	if !strings.Contains(text, "[CLI]") {
		t.Fatalf("未显示命令文本: %q", text)
	}
}

func TestManagedPasteConfirmedAndTracked(t *testing.T) {
	dir, link := managedLink(t, "")
	s, err := NewManaged(dir, link)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var buf strings.Builder
	go func() {
		data, _ := io.ReadAll(s.termRead)
		buf.WriteString(string(data))
	}()
	confirmed := false
	err = s.PasteText(context.Background(), "echo pasted-line", func() PasteTarget {
		return PasteTarget{Mode: PastePlain}
	}, func(context.Context) error {
		confirmed = true
		return nil
	})
	if err != nil || !confirmed {
		t.Fatalf("粘贴应经确认发送: %v", err)
	}
	// 粘贴不追加 Enter：此时不应执行。
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(buf.String(), "pasted-line\r\n") {
		t.Fatal("粘贴不应自动提交")
	}
	if r := s.Execute(context.Background(), "true"); r.Err == nil {
		t.Fatal("粘贴形成未提交输入时应拒绝执行")
	}
	if _, err := s.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	managedWait(t, s, func() bool { return s.humanPrompt && s.pending == 0 && !s.dirty })
	if _, code := managedRun(t, s, "true"); code != 0 {
		t.Fatalf("粘贴提交后应恢复执行: %d", code)
	}
}

func TestManagedPasteRejectedDuringTakeover(t *testing.T) {
	s := newManagedFixture(t)
	done := make(chan error, 1)
	go func() { done <- s.Execute(context.Background(), "sleep 30").Err }()
	managedWait(t, s, func() bool { return s.active != nil && s.active.started })
	s.TakeOver()
	err := s.PasteText(context.Background(), "echo no", func() PasteTarget { return PasteTarget{} }, func(context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "接管") {
		t.Fatalf("接管期间应拒绝粘贴: %v", err)
	}
	_, _ = s.Write([]byte{3})
	<-done
}

// 迟到 DONE 必须恢复 Agent 就绪与空闲计时（从 Agent 空闲起步，避免初始 READY 遮蔽）。
func TestManagedLateDoneRestoresAgentReady(t *testing.T) {
	s := newManagedFixture(t)
	managedRun(t, s, "true")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result := s.Execute(ctx, "sleep 0.5")
	if result.Err == nil || !strings.Contains(result.Err.Error(), "结果未知") {
		t.Fatalf("超时应报告结果未知: %v", result.Err)
	}
	managedWait(t, s, func() bool { return len(s.unconfirmed) == 0 })
	if _, code := managedRun(t, s, "true"); code != 0 {
		t.Fatalf("迟到完成通知后应恢复执行: %d", code)
	}
	s.mu.Lock()
	armed := s.idleCancel != nil
	s.mu.Unlock()
	if !armed {
		t.Fatal("迟到完成通知后应重新计时空闲交还")
	}
}

func TestManagedLateDoneUnderTakeover(t *testing.T) {
	s := newManagedFixture(t)
	managedRun(t, s, "true")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if result := s.Execute(ctx, "sleep 0.5"); result.Err == nil {
		t.Fatal("超时应报告结果未知")
	}
	s.TakeOver()
	managedWait(t, s, func() bool { return len(s.unconfirmed) == 0 && s.humanPrompt })
	if err := s.ReturnControl(); err != nil {
		t.Fatalf("人工接管后应能交还: %v", err)
	}
	managedWait(t, s, func() bool { return s.bashReady })
	if _, code := managedRun(t, s, "true"); code != 0 {
		t.Fatalf("交还后应恢复执行: %d", code)
	}
}

// 人工空提示符下的空回车也要消费 pending，否则后续执行被永久拒绝。
func TestManagedEmptyEnterConsumed(t *testing.T) {
	s := newManagedFixture(t)
	managedWait(t, s, func() bool { return s.humanPrompt })
	if _, err := s.Write([]byte{13}); err != nil {
		t.Fatal(err)
	}
	managedWait(t, s, func() bool { return s.humanPrompt && s.pending == 0 && !s.dirty })
	if _, code := managedRun(t, s, "true"); code != 0 {
		t.Fatalf("空回车后应能执行: %d", code)
	}
}

func TestManagedPasteCancelBeforeSend(t *testing.T) {
	dir, link := managedLink(t, "")
	s, err := NewManaged(dir, link)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		data, _ := io.ReadAll(s.termRead)
		buf.Write(data)
		close(done)
	}()
	blocked := make(chan struct{})
	s.mu.Lock()
	s.beforeWrite = func() { <-blocked }
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = s.PasteText(ctx, "echo never-pasted", func() PasteTarget {
		return PasteTarget{Mode: PastePlain}
	}, func(context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "未发送") || errors.Is(err, control.ErrPasteUnknown) {
		t.Fatalf("发送前取消应明确未发送: %v", err)
	}
	close(blocked)
	time.Sleep(300 * time.Millisecond)
	if _, code := managedRun(t, s, "true"); code != 0 {
		t.Fatalf("取消粘贴后会话应可用: %d", code)
	}
	_ = s.Close()
	<-done
	if strings.Contains(buf.String(), "never-pasted") {
		t.Fatal("取消后不得补发粘贴")
	}
}

func TestManagedPasteUnknownOnDisconnect(t *testing.T) {
	s := newManagedFixture(t)
	s.mu.Lock()
	s.beforeWrite = func() { _ = s.cmd.Process.Kill() }
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := s.PasteText(ctx, "echo x", func() PasteTarget {
		return PasteTarget{Mode: PastePlain}
	}, func(context.Context) error { return nil })
	if !errors.Is(err, control.ErrPasteUnknown) {
		t.Fatalf("已开始写入但断线应报告未知: %v", err)
	}
}

func TestManagedCloseDropsUnsent(t *testing.T) {
	s := newManagedFixture(t)
	blocked := make(chan struct{})
	s.mu.Lock()
	s.beforeWrite = func() { <-blocked }
	s.mu.Unlock()
	result := make(chan control.Result, 1)
	go func() { result <- s.Execute(context.Background(), "echo must-not-run") }()
	managedWait(t, s, func() bool { return s.active != nil })
	_ = s.Close()
	select {
	case r := <-result:
		if r.Err == nil || !strings.Contains(r.Err.Error(), "未执行") {
			t.Fatalf("关闭后未发送命令必须明确不执行: %v", r.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("关闭未结束未发送请求")
	}
	close(blocked)
}

func TestManagedCloseSentUnknown(t *testing.T) {
	s := newManagedFixture(t)
	result := make(chan error, 1)
	go func() { result <- s.Execute(context.Background(), "sleep 30").Err }()
	managedWait(t, s, func() bool { return s.active != nil && s.active.sent })
	_ = s.Close()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "未知") {
			t.Fatalf("已发送而无法确认应报告未知: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("关闭未结束已发送请求")
	}
}

func TestManagedCancelUnsentCleansOutput(t *testing.T) {
	s := newManagedFixture(t)
	blocked := make(chan struct{})
	s.mu.Lock()
	s.beforeWrite = func() { <-blocked }
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result := s.Execute(ctx, "sleep 1")
	if result.Err == nil || !strings.Contains(result.Err.Error(), "未发送") {
		t.Fatalf("发送前取消应明确未发送: %v", result.Err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "output-") {
			t.Fatalf("取消未发送请求后残留输出文件: %s", entry.Name())
		}
	}
	close(blocked)
}

func TestManagedTakeoverAfterReleaseBeforeCommand(t *testing.T) {
	s := newManagedFixture(t)
	queued := make(chan struct{})
	resume := make(chan struct{})
	defer close(resume)
	writes := 0
	s.mu.Lock()
	s.beforeWrite = func() {
		writes++
		if writes == 2 {
			close(queued)
			<-resume
		}
	}
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan control.Result, 1)
	go func() { done <- s.Execute(ctx, "true") }()
	select {
	case <-queued:
	case <-ctx.Done():
		t.Fatal("命令没有进入发送队列")
	}
	s.TakeOver()
	select {
	case r := <-done:
		if r.Output != nil {
			_ = r.Output.Close()
			_ = os.Remove(r.Output.Name())
		}
		if r.Err == nil || !strings.Contains(r.Err.Error(), "未执行") {
			t.Fatalf("切换消息已发送，但命令尚未执行: %v", r.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("接管未取消尚未发送的命令")
	}
}

func TestManagedPasteInvalidatedBeforeWrite(t *testing.T) {
	s := newManagedFixture(t)
	queued := make(chan struct{})
	resume := make(chan struct{})
	s.mu.Lock()
	s.beforeWrite = func() {
		select {
		case <-queued:
		default:
			close(queued)
		}
		<-resume
	}
	s.mu.Unlock()
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- s.PasteText(ctx, "echo obsolete", func() PasteTarget {
			return PasteTarget{Mode: PastePlain}
		}, func(context.Context) error { return nil })
	}()
	select {
	case <-queued:
	case <-ctx.Done():
		t.Fatal("粘贴没有进入发送队列")
	}
	_, err := s.Write([]byte("manual input"))
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, errPasteChanged) {
			t.Fatalf("人工输入应使旧粘贴确认失效: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("粘贴没有返回结果")
	}
}
