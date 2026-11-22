package control

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type pasteWorkspaceStub struct{ *workspaceStub }

func (*pasteWorkspaceStub) SupportsSessionPaste() bool { return true }

type recordingPaster struct {
	executorFunc
	received chan string
}

func (r recordingPaster) Paste(_ context.Context, text string, notify func() error) error {
	if err := notify(); err != nil {
		return err
	}
	r.received <- text
	return nil
}

func TestPasteProtocolRouteAndCapabilities(t *testing.T) {
	received := make(chan string, 2)
	receiver := recordingPaster{executorFunc: func(context.Context, string) Result { return Result{} }, received: received}
	w := &workspaceStub{sessions: map[string]Executor{"old-opaque-ID": receiver}, current: "old-opaque-ID"}
	text := "  中文\t内容\n\n  尾行\n"
	oldPath := readTestServer(t, w)
	if err := pasteSession(oldPath, "old-opaque-ID", strings.NewReader(text), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("旧窗口：%v", err)
	}
	path := readTestServer(t, &pasteWorkspaceStub{w})
	for _, id := range []string{"", "old", "closed"} {
		if err := pasteSession(path, id, strings.NewReader(text), io.Discard, io.Discard); err == nil {
			t.Fatalf("无效 ID 被接受：%q", id)
		}
	}
	if len(received) != 0 {
		t.Fatal("拒绝的粘贴到了终端")
	}
	var out, notify bytes.Buffer
	if err := pasteSession(path, "old-opaque-ID", strings.NewReader(text), &out, &notify); err != nil {
		t.Fatal(err)
	}
	if got := <-received; got != text || !strings.Contains(out.String(), "已发送粘贴") || !strings.Contains(notify.String(), "等待确认") {
		t.Fatalf("文本或状态不一致：%q %q %q", got, out.String(), notify.String())
	}
	if _, err := runSession(path, "old-opaque-ID", "true", 0, io.Discard); err != nil {
		t.Fatal("现有执行受影响：", err)
	}
	c, err := connectWorkspace(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.conn.Close()
	if err := c.send(request{Action: "session.paste", Text: text}); err != nil {
		t.Fatal(err)
	}
	if r, err := c.reply(); err != nil || !strings.Contains(r.Error, "完整会话 ID") {
		t.Fatalf("服务接受了省略目标：%+v %v", r, err)
	}
}
func TestPasteInputValidation(t *testing.T) {
	for _, bad := range []string{"", "\xff", "\x00", "a\x1b[200~", "a\x7f", "a\u0085", strings.Repeat("a", maxPasteBytes+1)} {
		if err := ValidatePasteText(bad); err == nil {
			t.Fatalf("接受了无效粘贴（%d 字节）", len(bad))
		}
	}
	if err := ValidatePasteText("\t 中文\r\n\n"); err != nil {
		t.Fatal(err)
	}
}

type unknownPaster struct{ executorFunc }

func (unknownPaster) Paste(context.Context, string, func() error) error { return ErrPasteUnknown }
func TestPasteProtocolUnknown(t *testing.T) {
	w := &pasteWorkspaceStub{&workspaceStub{sessions: map[string]Executor{"a": unknownPaster{}}}}
	err := pasteSession(readTestServer(t, w), "a", strings.NewReader("hello"), io.Discard, io.Discard)
	if !errors.Is(err, ErrPasteUnknown) {
		t.Fatalf("应保留未知状态：%v", err)
	}
}
