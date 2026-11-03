package shell

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func waitText(t *testing.T, f *fixture, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(f.screen(), text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("未看到 %q: %q", text, f.screen())
}

func remoteFixture(t *testing.T) *fixture {
	t.Helper()
	return remoteFixtureWithShell(t, "/bin/zsh")
}

func remoteFixtureWithShell(t *testing.T, shellPath string) *fixture {
	t.Helper()
	f := newFixture(t, shellPath)
	path := filepath.Join(t.TempDir(), "mock-ssh")
	script := `#!/bin/sh
printf 'WT_MENU>'
IFS= read -r selection
printf 'WT_PASSWORD>'
IFS= read -r password
printf '\nWT_REMOTE_LOGIN\n'
PS1='WT_REMOTE_PROMPT> '
export PS1
exec /bin/bash --noprofile --norc -i
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Write([]byte(f.s.quote(path) + "\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_MENU>")
	if _, err := f.s.Write([]byte("1\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_PASSWORD>")
	if _, err := f.s.Write([]byte("dummy\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_REMOTE_PROMPT>")
	return f
}

func attachFixture(t *testing.T, f *fixture) {
	t.Helper()
	code := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- f.s.Attach(context.Background(), "bash", func(script string) error {
			code <- script
			return nil
		})
	}()
	var script string
	select {
	case script = <-code:
	case <-time.After(3 * time.Second):
		t.Fatal("未收到接入命令")
	}
	if _, err := f.s.Write([]byte(script + "\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("接入未完成")
	}
}

func waitRemoteReady(t *testing.T, f *fixture) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.s.mu.Lock()
		ready := f.s.remote != nil && f.s.remote.ready && len(f.s.remote.idleOutput) == 0 && !f.s.remote.dirty && f.s.remote.pending == 0 && f.s.remote.orphan == nil
		f.s.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("远端未就绪: %q", f.screen())
}

func TestRemoteAttachAndShellState(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	for _, tc := range []struct {
		command string
		output  string
		code    int
	}{
		{"printf 'hello\\n'", "hello\r\n", 0},
		{"printf \"a'b\\n\"\nprintf 'next\\n'\n", "a'b\r\nnext\r\n", 0},
		{"false", "", 1},
		{"printf '%s' \"$?\"", "1", 0},
		{"export WT_REMOTE=ok; cd /tmp", "", 0},
		{"printf '%s ' \"$WT_REMOTE\"; pwd -P", "ok /private/tmp\r\n", 0},
	} {
		output, code, err := f.run(t, tc.command)
		if err != nil || code != tc.code || output != tc.output {
			t.Fatalf("远端命令 %q: output=%q code=%d error=%v", tc.command, output, code, err)
		}
	}
	if _, err := f.s.Write([]byte("exit\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.s.mu.Lock()
		local := f.s.remote == nil && f.s.ready && !f.s.dirty && f.s.pending == 0
		f.s.mu.Unlock()
		if local {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	output, code, err := f.run(t, "printf local")
	if err != nil || code != 0 || output != "local" {
		t.Fatalf("回到本地后不能执行: %q %d %v", output, code, err)
	}
}

func TestRemoteTimeoutRequiresTakeoverForInput(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result := f.s.Execute(ctx, "sleep 2")
	if result.Err == nil || !strings.Contains(result.Err.Error(), "结果未知") || result.Output != nil {
		t.Fatalf("远端超时结果错误: %+v", result)
	}
	f.s.mu.Lock()
	active, ready := f.s.active != nil, f.s.remote != nil && f.s.remote.ready
	f.s.mu.Unlock()
	if active || ready {
		t.Fatal("超时后输入未释放或远端被误标为就绪")
	}
	if !f.s.OnKey(0, ui.KeyA) {
		t.Fatal("结果未知时 Agent 仍持有控制权，应等待人类接管")
	}
	f.s.TakeOver()
	if f.s.OnKey(0, ui.KeyA) {
		t.Fatal("人类接管后 GUI 的人工按键仍被拦截")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if next := f.s.Execute(ctx2, "printf wrong"); next.Err == nil {
		t.Fatal("结果未知期间接受了新的远端命令")
	}
	if err := f.s.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
}

func TestRemoteManualInputAndBusyProgram(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	if _, err := f.s.Write([]byte("printf WT_MANUAL_")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.run(t, "printf wrong"); err == nil {
		t.Fatal("远端有未提交输入时接受了 CLI 命令")
	}
	if _, err := f.s.Write([]byte("OK\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_MANUAL_OK")
	waitRemoteReady(t, f)
	if _, err := f.s.Write([]byte("sleep 1\r")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.run(t, "printf wrong"); err == nil {
		t.Fatal("远端程序执行期间接受了 CLI 命令")
	}
	waitRemoteReady(t, f)
	if output, code, err := f.run(t, "printf resumed"); err != nil || code != 0 || output != "resumed" {
		t.Fatalf("人工命令后远端未恢复: %q %d %v", output, code, err)
	}
}

func TestRemoteAttachCancellationDoesNotWrite(t *testing.T) {
	f := remoteFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- f.s.Attach(ctx, "bash", func(script string) error {
			code <- script
			return nil
		})
	}()
	var script string
	select {
	case script = <-code:
	case <-time.After(time.Second):
		t.Fatal("未收到接入命令")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "取消") {
			t.Fatalf("取消接入结果错误: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消接入未返回")
	}
	if strings.Contains(f.screen(), script) {
		t.Fatal("接入命令被自动写入终端")
	}
	if f.s.OnKey(0, ui.KeyA) {
		t.Fatal("取消接入后人工输入被拦截")
	}
}

func TestAttachAtMenuDoesNotInject(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	path := filepath.Join(t.TempDir(), "mock-menu")
	script := `#!/bin/sh
printf 'WT_MENU>'
IFS= read -r selection
if [ "$selection" = 1 ]; then printf 'WT_MENU_ACCEPTED\n'; else printf 'WT_MENU_UNEXPECTED\n'; fi
printf 'WT_PASSWORD>'
IFS= read -r password
printf 'WT_PASSWORD_ACCEPTED\n'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Write([]byte(f.s.quote(path) + "\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_MENU>")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	code := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- f.s.Attach(ctx, "bash", func(script string) error {
			code <- script
			return nil
		})
	}()
	var bootstrap string
	select {
	case bootstrap = <-code:
	case <-time.After(time.Second):
		t.Fatal("未收到接入命令")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "超时") {
			t.Fatalf("未执行接入命令应超时: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("接入超时未返回")
	}
	if strings.Contains(f.screen(), bootstrap) || strings.Contains(f.screen(), "WT_MENU_UNEXPECTED") {
		t.Fatal("接入期间向菜单自动发送了输入")
	}
	if _, err := f.s.Write([]byte("1\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_PASSWORD>")
	if _, err := f.s.Write([]byte("dummy\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_PASSWORD_ACCEPTED")
}

func TestRemoteOrphanPromptDoesNotConsumeNewInput(t *testing.T) {
	r := &remoteSession{ready: false, pending: 1, orphan: &request{finished: true}}
	s := &Session{remote: r}
	s.controlRemote(r, "C:1")
	if !s.controlRemote(r, "P:1") || r.ready || r.pending != 1 {
		t.Fatalf("旧提示符误消费了新输入: %+v", r)
	}
	s.controlRemote(r, "C:2")
	if !s.controlRemote(r, "P:2") || !r.ready || r.pending != 0 {
		t.Fatalf("新命令完成后未恢复: %+v", r)
	}
}

func TestRemoteAttachRejectsExtraInput(t *testing.T) {
	f := remoteFixture(t)
	code := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- f.s.Attach(context.Background(), "bash", func(script string) error {
			code <- script
			return nil
		})
	}()
	var script string
	select {
	case script = <-code:
	case <-time.After(time.Second):
		t.Fatal("未收到接入命令")
	}
	if _, err := f.s.Write([]byte(script + "\rextra")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "其他输入") {
			t.Fatalf("接入时额外输入未被拒绝: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("额外输入后接入未结束")
	}
	if _, _, err := f.run(t, "printf wrong"); err == nil {
		t.Fatal("接入期间存在额外输入仍接受了 CLI 命令")
	}
}

func TestRemotePreservesPromptAndDebugTrap(t *testing.T) {
	f := remoteFixture(t)
	if _, err := f.s.Write([]byte("PROMPT_COMMAND='printf WT_EXISTING_HOOK'; trap ':' DEBUG\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_EXISTING_HOOK")
	attachFixture(t, f)
	output, code, err := f.run(t, `trap -p DEBUG; printf '%s' "$PROMPT_COMMAND"`)
	if err != nil || code != 0 || !strings.Contains(output, "trap -- ':' DEBUG") || !strings.Contains(output, "printf WT_EXISTING_HOOK") {
		t.Fatalf("原有提示符或 DEBUG trap 未保留: %q %d %v", output, code, err)
	}
}

func TestRemoteAttachPromptCommandEndingInSemicolon(t *testing.T) {
	f := remoteFixtureWithShell(t, "/bin/bash")
	if _, err := f.s.Write([]byte("PROMPT_COMMAND='printf WT_HOOK_''OK; '\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_HOOK_OK")
	attachFixture(t, f)
	output, code, err := f.run(t, "printf WT_RESULT")
	if err != nil || code != 0 || !strings.Contains(output, "WT_RESULT") {
		t.Fatalf("原有提示符命令末尾有分号时执行失败: %q %d %v", output, code, err)
	}
	if strings.Contains(f.screen(), "syntax error") {
		t.Fatalf("提示符命令产生语法错误: %q", f.screen())
	}
}

func TestAttachInputErrorDistinguishesPendingKeys(t *testing.T) {
	r := &remoteSession{bootstrap: "example", input: []byte("example\r")}
	s := &Session{keys: 2, submitted: 1}
	if err := r.attachInputError(s); err == nil || !strings.Contains(err.Error(), "按键尚未提交") {
		t.Fatalf("未识别未提交按键: %v", err)
	}
	s.submitted = s.keys
	if err := r.attachInputError(s); err != nil {
		t.Fatalf("完整输入应被接受: %v", err)
	}
	r.input = []byte("example\rextra")
	if err := r.attachInputError(s); err == nil || !strings.Contains(err.Error(), "输入 13 字节，期望 8 字节，首处差异位置 9") {
		t.Fatalf("未拒绝额外输入: %v", err)
	}
}

func TestRemotePreservesPromptArray(t *testing.T) {
	f := remoteFixture(t)
	if _, err := f.s.Write([]byte("PROMPT_COMMAND=('printf WT_ARRAY_''FIRST' 'printf WT_ARRAY_SECOND')\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_ARRAY_FIRST")
	attachFixture(t, f)
	output, code, err := f.run(t, `declare -p PROMPT_COMMAND`)
	if err != nil || code != 0 || !strings.Contains(output, "printf WT_ARRAY_FIRST") || !strings.Contains(output, "printf WT_ARRAY_SECOND") {
		t.Fatalf("原有 PROMPT_COMMAND 数组未保留: %q %d %v", output, code, err)
	}
}

func TestRemotePromptTextAndRewrite(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	if output, code, err := f.run(t, `PS1='WT_NEW_''VISIBLE> '`); err != nil || code != 0 || output != "" {
		t.Fatalf("更改 PS1 失败: %q %d %v", output, code, err)
	}
	waitText(t, f, "WT_NEW_VISIBLE>")
	if output, code, err := f.run(t, "false"); err != nil || code != 1 || output != "" {
		t.Fatalf("更改提示符后的状态错误: %q %d %v", output, code, err)
	}
	if output, code, err := f.run(t, `printf '%s' "$?"`); err != nil || code != 0 || output != "1" {
		t.Fatalf("更改提示符后未保留退出状态: %q %d %v", output, code, err)
	}
}

func TestRemoteReadOnlyPromptRejected(t *testing.T) {
	f := remoteFixture(t)
	if _, err := f.s.Write([]byte("readonly PS1; printf '%s%s' WT_READONLY_ PROMPT\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_READONLY_PROMPT")
	code := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- f.s.Attach(context.Background(), "bash", func(script string) error {
			code <- script
			return nil
		})
	}()
	select {
	case script := <-code:
		if _, err := f.s.Write([]byte(script + "\r")); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到接入命令")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "不可修改") {
			t.Fatalf("只读 PS1 未被拒绝: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("只读 PS1 导致接入未结束")
	}
}

func TestRemotePromptvarsDisabledRejected(t *testing.T) {
	f := remoteFixture(t)
	if _, err := f.s.Write([]byte("shopt -u promptvars; printf '%s%s' WT_NO_ PROMPTVARS\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_NO_PROMPTVARS")
	code := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- f.s.Attach(context.Background(), "bash", func(script string) error {
			code <- script
			return nil
		})
	}()
	select {
	case script := <-code:
		if _, err := f.s.Write([]byte(script + "\r")); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到接入命令")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "promptvars") {
			t.Fatalf("关闭 promptvars 未被拒绝: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("关闭 promptvars 导致接入未结束")
	}
}

func TestRemotePromptvarsDisabledAfterAttach(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	if output, code, err := f.run(t, "shopt -u promptvars"); err != nil || code != 0 || output != "" {
		t.Fatalf("关闭 promptvars 后命令结果错误: %q %d %v", output, code, err)
	}
	if _, _, err := f.run(t, "printf wrong"); err == nil {
		t.Fatal("关闭 promptvars 后接受了新的命令")
	}
}

func TestRemoteTimeoutRecoversAfterCompletion(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := f.s.Execute(ctx, "sleep 0.4")
	if result.Err == nil || !strings.Contains(result.Err.Error(), "结果未知") {
		t.Fatalf("超时未报告未知结果: %+v", result)
	}
	waitRemoteReady(t, f)
	if output, code, err := f.run(t, "printf resumed"); err != nil || code != 0 || output != "resumed" {
		t.Fatalf("收到完成标记后未恢复: %q %d %v", output, code, err)
	}
}

func TestRemoteTimeoutInterruptManualCommandAndExit(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result := f.s.Execute(ctx, "sleep 5; printf TOO_LATE")
	if result.Err == nil || !strings.Contains(result.Err.Error(), "结果未知") {
		t.Fatalf("超时未报告未知结果: %+v", result)
	}
	if f.s.OnKey(ui.Ctrl, ui.KeyC) {
		t.Fatal("超时后 Ctrl+C 被拦截")
	}
	if _, err := f.s.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "TOO_LATE")
	f.s.TakeOver()
	if _, err := f.s.Write([]byte("printf '%s%s' WT_GUI_ AFTER_TIMEOUT\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_GUI_AFTER_TIMEOUT")
	if _, err := f.s.Write([]byte("exit\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.s.mu.Lock()
		local := f.s.remote == nil
		f.s.mu.Unlock()
		if local {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.waitReady(t)
	if err := f.s.ReturnControl(); err != nil {
		t.Fatalf("确认本地提示符后无法交还: %v", err)
	}
	if output, code, err := f.run(t, "printf local"); err != nil || code != 0 || output != "local" {
		t.Fatalf("退出远端后本地终端未恢复: %q %d %v", output, code, err)
	}
}

func TestRemoteDisconnectToMenuRejectsCommands(t *testing.T) {
	f := newFixture(t, "/bin/zsh")
	path := filepath.Join(t.TempDir(), "mock-bastion")
	script := `#!/bin/bash
set -m
PS1='WT_REMOTE_PROMPT> '
export PS1
/bin/bash --noprofile --norc -i
printf '\nWT_BACK_MENU>'
IFS= read -r selection
if [ "$selection" = 0 ]; then printf 'WT_MENU_CLEAN\n'; else printf 'WT_MENU_INJECTED\n'; fi
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Write([]byte(f.s.quote(path) + "\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_REMOTE_PROMPT>")
	attachFixture(t, f)
	output, code, err := f.run(t, `printf '%s' "$$"`)
	if err != nil || code != 0 {
		t.Fatalf("读取测试 Bash 进程号失败: %q %d %v", output, code, err)
	}
	pid, err := strconv.Atoi(output)
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_BACK_MENU>")
	if output, code, err := f.run(t, "printf wrong"); err == nil {
		f.s.mu.Lock()
		remote := f.s.remote
		f.s.mu.Unlock()
		t.Fatalf("远端退出并出现菜单后仍接受 CLI 命令: %q %d remote=%+v screen=%q", output, code, remote, f.screen())
	}
	f.s.TakeOver()
	if _, err := f.s.Write([]byte("0\n")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_MENU_CLEAN")
	if strings.Contains(f.screen(), "WT_MENU_INJECTED") {
		t.Fatal("远端断线后 CLI 输入被发送到菜单")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.s.mu.Lock()
		local := f.s.remote == nil
		f.s.mu.Unlock()
		if local {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, err := f.run(t, "printf wrong"); err == nil {
		t.Fatal("菜单退出后尚不能确认输入状态时接受了 CLI 命令")
	}
	if f.s.OnKey(0, ui.KeyEnter) {
		t.Fatal("返回本地后无法手动确认空提示符")
	}
	if _, err := f.s.Write([]byte("\r")); err != nil {
		t.Fatal(err)
	}
	f.waitReady(t)
	if err := f.s.ReturnControl(); err != nil {
		t.Fatalf("确认本地提示符后无法交还: %v", err)
	}
	if output, code, err := f.run(t, "printf local"); err != nil || code != 0 || output != "local" {
		t.Fatalf("本地空提示符确认后未恢复: %q %d %v", output, code, err)
	}
}

func TestRemotePromptSuffixControlAcrossReads(t *testing.T) {
	r := &remoteSession{ready: true}
	for _, b := range []byte("\x1b[?2004h\x1b[K") {
		r.observeOutput([]byte{b})
		if !r.ready {
			t.Fatal("拆分的提示符控制序列被判为未知状态")
		}
	}
	if len(r.idleOutput) != 0 {
		t.Fatal("完整控制序列仍有未处理字节")
	}
	r.observeOutput([]byte("\r\nMENU>"))
	if r.ready {
		t.Fatal("提示符后出现菜单文本时仍为就绪状态")
	}
}

func TestRemoteExitReportsIncompleteAndRestoresLocal(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	output, _, err := f.run(t, "printf before; exit 0")
	if err == nil || !strings.HasPrefix(output, "before") {
		t.Fatalf("远端退出后结果错误: %q %v", output, err)
	}
	if output, code, err := f.run(t, "printf local"); err != nil || code != 0 || output != "local" {
		t.Fatalf("远端退出后本地终端未恢复: %q %d %v", output, code, err)
	}
}

func TestRemoteInterruptCommand(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	type outcome struct {
		code int
		err  error
	}
	result := make(chan outcome, 1)
	go func() {
		_, code, err := f.run(t, "sleep 60")
		result <- outcome{code, err}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.s.mu.Lock()
		started := f.s.active != nil && f.s.active.started
		f.s.mu.Unlock()
		if started {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if f.s.OnKey(ui.Ctrl, ui.KeyC) {
		t.Fatal("远端命令执行期间阻止了 Ctrl+C")
	}
	if _, err := f.s.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err == nil && got.code == 0 {
			t.Fatal("中断的远端命令被报告为成功")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("中断后远端命令未返回")
	}
	if output, code, err := f.run(t, "printf resumed"); err != nil || code != 0 || output != "resumed" {
		t.Fatalf("中断后不能继续使用远端 Shell: %q %d %v", output, code, err)
	}
}

func TestRemoteRemovedPromptHookDoesNotHang(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	start := time.Now()
	output, code, err := f.run(t, "unset PROMPT_COMMAND")
	if err != nil || code != 0 || output != "" || time.Since(start) > 4*time.Second {
		t.Fatalf("移除远端提示符钩子后没有返回已完成命令: %q %d %v", output, code, err)
	}
	if _, _, err := f.run(t, "printf wrong"); err == nil {
		t.Fatal("远端提示符状态未知时接受了新的命令")
	}
}

func TestRemotePromptExpansionIsNotReady(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	if _, err := f.s.Write([]byte("PS1='$(printf \"%s%s\" WT_PROMPT_ RUNNING >&2; read -r WT_PROMPT_INPUT; printf READY) '\r")); err != nil {
		t.Fatal(err)
	}
	waitText(t, f, "WT_PROMPT_RUNNING")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result := f.s.Execute(ctx, "printf wrong")
	if result.Err == nil || strings.Contains(result.Err.Error(), "结果未知") {
		t.Fatalf("提示符仍在读取输入时接受了 CLI 命令: %+v", result)
	}
}

func TestRemoteOldPromptMarkerIsNotReady(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	if output, code, err := f.run(t, `unset PROMPT_COMMAND; PS1="${PS1}"'$(printf "%s%s" WT_OLD_ PROMPT >&2; read -r WT_PROMPT_INPUT; printf READY)'`); err != nil || code != 0 || output != "" {
		t.Fatalf("更改提示符命令失败: %q %d %v", output, code, err)
	}
	waitText(t, f, "WT_OLD_PROMPT")
	if _, _, err := f.run(t, "printf wrong"); err == nil {
		t.Fatal("旧的 PS1 标记被误认为新的空闲提示符")
	}
}

func TestRemoteInteractiveInputRequiresReattach(t *testing.T) {
	f := remoteFixture(t)
	attachFixture(t, f)
	if _, err := f.s.Write([]byte("read WT_SECRET\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Write([]byte("dummy\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.s.mu.Lock()
		pending := f.s.remote != nil && f.s.remote.pending == 1
		f.s.mu.Unlock()
		if pending {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, err := f.run(t, "printf wrong"); err == nil {
		t.Fatal("交互程序输入后无法确认状态时接受了 CLI 命令")
	}
	attachFixture(t, f)
	if output, code, err := f.run(t, `printf '%s' "$WT_SECRET"`); err != nil || code != 0 || output != "dummy" {
		t.Fatalf("重新接入后状态或命令错误: %q %d %v", output, code, err)
	}
}

func TestRemotePasteNewlinesAreNotSubmissions(t *testing.T) {
	r := &remoteSession{ready: true}
	s := &Session{remote: r}
	// 粘贴边界可以被输入队列拆开，粘贴内的换行不是提交按键。
	for _, b := range []byte("\x1b[200~(\nprintf ok\n)\x1b[201~") {
		r.trackInput(s, []byte{b})
	}
	if r.pending != 0 || !r.dirty {
		t.Fatalf("尚未提交的粘贴状态错误: pending=%d dirty=%v", r.pending, r.dirty)
	}
	r.trackInput(s, []byte("\r"))
	s.controlRemote(r, "C:1")
	s.controlRemote(r, "P:1")
	if !r.ready || r.pending != 0 || r.dirty {
		t.Fatalf("粘贴执行后的提示符未恢复: pending=%d dirty=%v ready=%v", r.pending, r.dirty, r.ready)
	}
}

func TestRemoteRawOutputWithLocalFish(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish 不可用")
	}
	f := remoteFixtureWithShell(t, fish)
	attachFixture(t, f)
	output, code, err := f.run(t, `printf '\033]7;file://remote/tmp\007\000X'; head -c 1048576 /dev/zero | tr '\000' x`)
	wantPrefix := "\x1b]7;file://remote/tmp\a\x00X"
	if err != nil || code != 0 || !strings.HasPrefix(output, wantPrefix) || len(output) != len(wantPrefix)+1048576 || strings.Trim(output[len(wantPrefix):], "x") != "" {
		t.Fatalf("远端原始输出不完整: 长度 %d，退出码 %d，错误 %v", len(output), code, err)
	}
}

func TestRemoteMarkerSplitAcrossReads(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "output-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	remote := &remoteSession{marker: []byte("\x1eWT:remote:")}
	request := &request{output: output, done: make(chan struct{}), remote: true}
	s := &Session{initSeen: true, initial: true, marker: []byte("\x1eWT:local:"), remote: remote, active: request}
	input := []byte("hidden\x1eWT:remote:S\x1ffirst\x00second\x1eWT:remote:E:7\x1f\x1eWT:remote:C:1\x1f\x1eWT:remote:P:1\x1ftail")
	for _, b := range input {
		s.readBuf = append(s.readBuf, b)
		s.parse(false)
	}
	if s.active != nil || remote.ready {
		t.Fatal("拆分的远端标记未完成请求或后续输出未标记为未知")
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(output)
	if err != nil || string(data) != "first\x00second" || request.result.ExitCode != 7 {
		t.Fatalf("拆分标记输出错误: %q，退出码 %d，错误 %v", data, request.result.ExitCode, err)
	}
}
