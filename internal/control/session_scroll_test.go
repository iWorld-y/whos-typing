package control

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type scrollTestConn struct {
	closed chan struct{}
	once   sync.Once
	writes atomic.Int32
}

func (c *scrollTestConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *scrollTestConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	return len(p), nil
}

func (c *scrollTestConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func TestSessionReadWhileViewportScrolled(t *testing.T) {
	if os.Getenv("MYGO_GHOSTTY_VT") == "" {
		t.Skip("需要 MYGO_GHOSTTY_VT 指向上游终端库")
	}
	conn := &scrollTestConn{closed: make(chan struct{})}
	term, err := terminal.New(terminal.Options{Conn: conn, NoBlink: true, Keybinds: []terminal.Keybind{{Mods: ui.Ctrl, Key: ui.KeyHome, Action: terminal.ActionScrollToTop}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })
	tester := ui.NewTester(func(c *ui.Context) { terminal.View(c, term).Fill().AutoFocus() }, 400, 200)
	lines := make([]string, 400)
	for i := range lines {
		lines[i] = fmt.Sprintf("LINE_%03d %s", i, strings.Repeat("x", 40))
		term.Feed([]byte(lines[i] + "\r\n"))
	}
	tester.Frame()
	full := term.Text()
	if full != strings.Join(lines, "\n") {
		t.Fatal("普通屏幕中的滚动历史未完整保留")
	}
	path := readTestServer(t, readableExecutor{read: func() (string, error) { return term.Text(), nil }})
	read := func() string {
		t.Helper()
		var output bytes.Buffer
		if err := readSession(path, &output); err != nil {
			t.Fatal(err)
		}
		return output.String()
	}
	want := strings.Join(lines[200:], "\n")
	if got := read(); got != want {
		t.Fatalf("底部读取的最新历史错误: 字节 %d，期望 %d", len(got), len(want))
	}
	for range 5 {
		tester.Key(ui.Shift, ui.KeyPageUp)
	}
	tester.Key(ui.Ctrl, ui.KeyHome)
	tester.Frame()
	cols, rows := term.Size()
	cellWidth, cellHeight := float32(400)/float32(cols), float32(200)/float32(rows)
	tester.Press(cellWidth/2, cellHeight/2)
	tester.Move(cellWidth*9.5, cellHeight/2)
	tester.Release(cellWidth*9.5, cellHeight/2)
	tester.Command("copy")
	if top := tester.Clipboard(); top != "LINE_000" {
		t.Fatalf("测试视口首行 = %q，期望滚动历史顶部", top)
	}
	if term.Text() != full {
		t.Fatal("视口滚动改变了终端历史快照")
	}
	if got := read(); got != want {
		t.Fatalf("滚动后读取的最新历史错误: 字节 %d，期望 %d", len(got), len(want))
	}
	tester.SetClipboard("reset")
	tester.Command("copy")
	if selected := tester.Clipboard(); selected != "LINE_000" {
		t.Fatalf("只读请求改变了终端选区: %q", selected)
	}
	term.Feed([]byte(strings.Repeat("\r\n", 200)))
	if got := read(); got != want {
		t.Fatal("尾部空行影响了最新有效历史的截取")
	}
	if conn.writes.Load() != 0 {
		t.Fatal("只读请求向终端输入了内容")
	}
}
