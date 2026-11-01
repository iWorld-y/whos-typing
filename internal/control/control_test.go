package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type executorFunc func(context.Context, string) Result

func (f executorFunc) Execute(ctx context.Context, command string) Result {
	return f(ctx, command)
}

type attachExecutor struct {
	attach func(context.Context, string, func(string) error) error
}

type autoAttachExecutor struct {
	attach func(context.Context, string, func() error) error
}

func (a autoAttachExecutor) Execute(context.Context, string) Result {
	return Result{Err: errors.New("不应执行命令")}
}

func (a autoAttachExecutor) AttachAuto(ctx context.Context, shell string, notify func() error) error {
	return a.attach(ctx, shell, notify)
}

func (a attachExecutor) Execute(context.Context, string) Result {
	return Result{Err: errors.New("不应执行命令")}
}

func (a attachExecutor) Attach(ctx context.Context, shell string, show func(string) error) error {
	return a.attach(ctx, shell, show)
}

func testDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wt-control-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestUnavailableAndDisconnected(t *testing.T) {
	dir := testDir(t)
	start := time.Now()
	if _, err := run(socketPath(dir), "pwd", io.Discard); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("未启动时应快速报错: %v", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath(dir), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.AcceptUnix()
		if err == nil {
			_, _ = bufio.NewReader(conn).ReadBytes('\n')
			_ = conn.Close()
		}
	}()
	if code, err := run(socketPath(dir), "pwd", io.Discard); err == nil || code == 0 {
		t.Fatalf("异常断连不得报告成功: 退出码 %d，错误 %v", code, err)
	}
}

func TestClientTimeoutWhenServerDoesNotRespond(t *testing.T) {
	dir := testDir(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath(dir), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		_, _ = io.Copy(io.Discard, conn)
	}()
	start := time.Now()
	code, err := runTimed(socketPath(dir), "sleep 30", 100*time.Millisecond, io.Discard)
	if err == nil || code == 0 || !strings.Contains(err.Error(), "结果未知") || time.Since(start) > 2*time.Second {
		t.Fatalf("服务未响应时超时结果错误: code=%d error=%v elapsed=%s", code, err, time.Since(start))
	}
}

func TestAttachProtocolAndCancellation(t *testing.T) {
	dir := testDir(t)
	server, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{})
	go server.Serve(attachExecutor{attach: func(ctx context.Context, shell string, show func(string) error) error {
		if shell != "bash" {
			return errors.New("不支持的 Shell")
		}
		if err := show("single-line-bootstrap"); err != nil {
			return err
		}
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}})
	conn, err := net.Dial("unix", socketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(request{Action: "attach", Shell: "bash"}); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var reply response
	if err := json.Unmarshal(line, &reply); err != nil || reply.Phase != "instructions" || reply.Script != "single-line-bootstrap" {
		t.Fatalf("接入指令错误: %+v %v", reply, err)
	}
	_ = conn.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("客户端断开后接入未取消")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAttachClientReceivesInstructionsAndCompletion(t *testing.T) {
	dir := testDir(t)
	server, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(attachExecutor{attach: func(_ context.Context, _ string, show func(string) error) error {
		return show("single-line-bootstrap")
	}})
	defer server.Close()
	var output bytes.Buffer
	if err := attachRemote(socketPath(dir), "bash", &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "single-line-bootstrap") || !strings.Contains(output.String(), "已接入") {
		t.Fatalf("接入输出不完整: %q", output.String())
	}
}

func TestAutomaticAttachClientWaitsForConfirmation(t *testing.T) {
	dir := testDir(t)
	server, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	prompted := make(chan struct{})
	confirmed := make(chan struct{})
	go server.Serve(autoAttachExecutor{attach: func(ctx context.Context, shell string, notify func() error) error {
		if shell != "bash" {
			return errors.New("不支持的 Shell")
		}
		if err := notify(); err != nil {
			return err
		}
		close(prompted)
		select {
		case <-confirmed:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	defer server.Close()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- attachRemoteAuto(socketPath(dir), "bash", &output) }()
	select {
	case <-prompted:
	case <-time.After(time.Second):
		t.Fatal("未收到确认请求")
	}
	select {
	case err := <-done:
		t.Fatalf("用户确认前接入已结束: %v", err)
	default:
	}
	close(confirmed)
	if err := <-done; err != nil {
		t.Fatalf("确认后接入失败: %v", err)
	}
	if !strings.Contains(output.String(), "窗口") || !strings.Contains(output.String(), "已接入") {
		t.Fatalf("自动接入输出不完整: %q", output.String())
	}
}

func TestAutomaticAttachClientRejectsManualInstructions(t *testing.T) {
	dir := testDir(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath(dir), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		_ = json.NewEncoder(conn).Encode(response{Phase: "instructions", Script: "unexpected-bootstrap"})
	}()
	var output bytes.Buffer
	if err := attachRemoteAuto(socketPath(dir), "bash", &output); err == nil {
		t.Fatal("自动接入不应接受手动接入指令")
	}
	if strings.Contains(output.String(), "unexpected-bootstrap") {
		t.Fatal("自动接入不应输出手动接入命令")
	}
}

func TestAutomaticAttachClientReportsOlderWindow(t *testing.T) {
	dir := testDir(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath(dir), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		_ = json.NewEncoder(conn).Encode(response{Error: "命令请求格式错误"})
	}()
	err = attachRemoteAuto(socketPath(dir), "bash", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "不支持自动接入") {
		t.Fatalf("旧窗口应提示版本不支持: %v", err)
	}
}

func TestAutomaticAttachDisconnectCancelsConfirmation(t *testing.T) {
	dir := testDir(t)
	server, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{})
	go server.Serve(autoAttachExecutor{attach: func(ctx context.Context, _ string, notify func() error) error {
		if err := notify(); err != nil {
			return err
		}
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}})
	defer server.Close()
	conn, err := net.Dial("unix", socketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(request{Action: "attach.auto", Shell: "bash"}); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var reply response
	if err := json.Unmarshal(line, &reply); err != nil || reply.Phase != "confirmation" {
		t.Fatalf("确认请求错误: %+v %v", reply, err)
	}
	_ = conn.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("CLI 断开后未取消窗口确认")
	}
}

func TestSocketPermissionsAndLargeBinaryOutput(t *testing.T) {
	dir := testDir(t)
	server, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0, 1, 'x', '\n', 0xff}, 300000)
	go server.Serve(executorFunc(func(_ context.Context, command string) Result {
		if command != "test-command" {
			return Result{Err: errors.New("命令不匹配")}
		}
		f, err := os.CreateTemp(dir, "response-")
		if err != nil {
			return Result{Err: err}
		}
		if _, err := f.Write(data); err != nil {
			return Result{Output: f, Err: err}
		}
		_, err = f.Seek(0, io.SeekStart)
		return Result{Output: f, ExitCode: 7, Err: err}
	}))
	defer server.Close()
	if info, err := os.Lstat(dir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("目录权限错误: %v %v", info, err)
	}
	if info, err := os.Lstat(socketPath(dir)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket 权限错误: %v %v", info, err)
	}
	if other, err := listen(dir); err == nil {
		go other.Serve(nil)
		_ = other.Close()
		t.Fatal("第二个实例未被拒绝")
	}
	var output bytes.Buffer
	code, err := run(socketPath(dir), "test-command", &output)
	if err != nil || code != 7 || !bytes.Equal(output.Bytes(), data) {
		t.Fatalf("大二进制输出不一致: 长度 %d，退出码 %d，错误 %v", output.Len(), code, err)
	}
}

func TestUnsafeDirectoryRejected(t *testing.T) {
	dir := testDir(t)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := listen(dir); err == nil || !strings.Contains(err.Error(), "权限不安全") {
		t.Fatalf("不安全目录应被拒绝: %v", err)
	}
}

func TestSessionClosesDuringRequest(t *testing.T) {
	dir := testDir(t)
	server, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	closed := make(chan struct{})
	go server.Serve(executorFunc(func(_ context.Context, _ string) Result {
		close(started)
		<-closed
		return Result{Err: errors.New("终端会话已结束")}
	}))
	result := make(chan error, 1)
	go func() {
		code, err := run(socketPath(dir), "sleep 60", io.Discard)
		if err == nil || code == 0 {
			result <- errors.New("窗口关闭后误报成功")
			return
		}
		result <- nil
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("请求未到达服务端")
	}
	close(closed)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("窗口关闭后请求未结束")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseDoesNotWaitForUnreadOutput(t *testing.T) {
	dir := testDir(t)
	server, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	go server.Serve(executorFunc(func(_ context.Context, _ string) Result {
		f, err := os.CreateTemp(dir, "response-")
		if err != nil {
			return Result{Err: err}
		}
		if _, err := f.Write(bytes.Repeat([]byte("x"), 8<<20)); err != nil {
			return Result{Output: f, Err: err}
		}
		_, err = f.Seek(0, io.SeekStart)
		close(started)
		return Result{Output: f, Err: err}
	}))
	conn, err := net.Dial("unix", socketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request{Command: "large-output"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("请求未开始")
	}
	done := make(chan error, 1)
	go func() { done <- server.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("客户端未读取输出时服务未能关闭")
	}
}

func TestExistingNonSocketPreserved(t *testing.T) {
	dir := testDir(t)
	path := socketPath(dir)
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := listen(dir); err == nil {
		t.Fatal("已有普通文件应导致失败")
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil || string(data) != "keep" {
		t.Fatalf("已有文件被修改: %q %v", data, err)
	}
}
