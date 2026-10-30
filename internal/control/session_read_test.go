package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type readableExecutor struct {
	read    func() (string, error)
	execute func(context.Context, string) Result
}

func (e readableExecutor) Execute(ctx context.Context, command string) Result {
	if e.execute == nil {
		return Result{Err: errors.New("不应执行命令")}
	}
	return e.execute(ctx, command)
}

func (e readableExecutor) ReadSession() (string, error) { return e.read() }

func readTestServer(t *testing.T, executor Executor) string {
	t.Helper()
	dir := testDir(t)
	server, err := listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(executor)
	t.Cleanup(func() { _ = server.Close() })
	return socketPath(dir)
}

func TestSessionReadUnicodeLimit(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
	}{
		{"empty", "", ""},
		{"short", "plain 中文😀", "plain 中文😀"},
		{"invalid_utf8", "a\xffb", "a\uFFFDb"},
		{"exact", strings.Repeat("a", 10000), strings.Repeat("a", 10000)},
		{"ascii", strings.Repeat("a", 10000) + "z", strings.Repeat("a", 9999) + "z"},
		{"chinese", strings.Repeat("中", 10000) + "尾", strings.Repeat("中", 9999) + "尾"},
		{"emoji", strings.Repeat("😀", 10000) + "🌟", strings.Repeat("😀", 9999) + "🌟"},
		{"mixed", "前" + strings.Repeat("a中😀", 3334), "😀" + strings.Repeat("a中😀", 3333)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := readTestServer(t, readableExecutor{read: func() (string, error) { return tc.text, nil }})
			var output bytes.Buffer
			if err := readSession(path, &output); err != nil {
				t.Fatal(err)
			}
			if output.String() != tc.want || !utf8.Valid(output.Bytes()) || utf8.RuneCount(output.Bytes()) > 10000 {
				t.Fatalf("读取末尾错误: 字节 %d，字符 %d，UTF-8=%v", output.Len(), utf8.RuneCount(output.Bytes()), utf8.Valid(output.Bytes()))
			}
		})
	}
}

func TestSessionReadTrimsOnlyEdgeNewlines(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
	}{
		{"edges", "\r\n\n  中文\n 😀  \r\n\r", "  中文\n 😀  "},
		{"only_newlines", "\r\n\n\r", ""},
		{"trailing_before_limit", strings.Repeat("中", 10000) + strings.Repeat("\r\n", 100), strings.Repeat("中", 10000)},
		{"leading_after_limit", "X\r\n\r\n" + strings.Repeat("😀", 9998), strings.Repeat("😀", 9998)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := readTestServer(t, readableExecutor{read: func() (string, error) { return tc.text, nil }})
			var output bytes.Buffer
			if err := readSession(path, &output); err != nil {
				t.Fatal(err)
			}
			if output.String() != tc.want || !utf8.Valid(output.Bytes()) || utf8.RuneCount(output.Bytes()) > 10000 {
				t.Fatalf("换行或截取错误: 字节 %d，字符 %d", output.Len(), utf8.RuneCount(output.Bytes()))
			}
		})
	}
}

func TestSessionReadEmptyAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		exec Executor
		want string
	}{
		{"closed", readableExecutor{read: func() (string, error) { return "", errors.New("终端会话已结束") }}, "已结束"},
		{"not_started", nil, "未启动"},
		{"unsupported", executorFunc(func(context.Context, string) Result { return Result{} }), "不支持"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := readTestServer(t, tc.exec)
			if err := readSession(path, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("读取应报告 %q: %v", tc.want, err)
			}
		})
	}
	if err := readSession(socketPath(testDir(t)), io.Discard); err == nil {
		t.Fatal("GUI 未启动时读取成功")
	}
}

func TestSessionReadDuringCommandDoesNotExecute(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var count atomic.Int32
	path := readTestServer(t, readableExecutor{
		read: func() (string, error) { return "执行中\nvisible", nil },
		execute: func(ctx context.Context, command string) Result {
			if count.Add(1) != 1 || command != "session read" {
				return Result{Err: errors.New("读取触发了额外命令或原命令改变")}
			}
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Result{Err: ctx.Err()}
			}
			f, err := os.CreateTemp(t.TempDir(), "output-")
			if err != nil {
				return Result{Err: err}
			}
			if _, err := f.WriteString("result"); err != nil {
				return Result{Output: f, Err: err}
			}
			_, err = f.Seek(0, io.SeekStart)
			return Result{Output: f, ExitCode: 7, Err: err}
		},
	})
	done := make(chan error, 1)
	go func() {
		var output bytes.Buffer
		code, err := run(path, "session read", &output)
		if err != nil || code != 7 || output.String() != "result" {
			done <- errors.New("原有命令协议结果不正确")
			return
		}
		done <- nil
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("原命令未开始")
	}
	var output bytes.Buffer
	if err := readSession(path, &output); err != nil || output.String() != "执行中\nvisible" || count.Load() != 1 {
		t.Fatalf("执行中读取失败或执行了其他命令: %q %v", output.String(), err)
	}
	release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("原命令未完成")
	}
}
