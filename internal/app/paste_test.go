package app

import (
	"context"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"whos-typing/internal/shell"
)

func TestPasteModeFromNativeSnapshot(t *testing.T) {
	requireTerminal(t)
	conn := &passiveConn{closed: make(chan struct{})}
	term, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	for _, v := range []struct {
		seq  string
		want shell.PasteTarget
	}{
		{"", shell.PasteTarget{Mode: shell.PastePlain}},
		{"\x1b[?2004h", shell.PasteTarget{Mode: shell.PasteBracketed}},
		{"\x1b[?2004l", shell.PasteTarget{Mode: shell.PastePlain}},
		{"\x1b[?2004;5522h", shell.PasteTarget{Mode: shell.PasteBracketed, ClipboardOffer: true}},
		{"\x1bc", shell.PasteTarget{Mode: shell.PastePlain}},
	} {
		term.Feed([]byte(v.seq))
		if got := pasteMode(term.Snapshot()); got != v.want {
			t.Fatalf("%q：%+v", v.seq, got)
		}
	}
}
func TestPasteModeControlString(t *testing.T) {
	for _, v := range []struct {
		snapshot string
		want     shell.PasteMode
	}{
		{"", shell.PasteUnknown},
		{"\x1b[?2004", shell.PasteUnknown},
		{"\x1b]8;;https://example.test/\x1b[?2004h\x1b\\text\x1b[0m", shell.PastePlain},
	} {
		if got := pasteMode([]byte(v.snapshot)); got.Mode != v.want {
			t.Fatalf("%q：%+v", v.snapshot, got)
		}
	}
	if !isPasswordPrompt("abc\nuser@localhost's password: ") || !isPasswordPrompt("密码：") || isPasswordPrompt("password: Canceled\nmysql>") {
		t.Fatal("错误识别密码提示")
	}
}
func TestPasteConfirmationIsOnlyInSelectedPane(t *testing.T) {
	requireTerminal(t)
	w := newWorkspace(testIDs(t), func(id string, onExit func(int)) (*pane, error) {
		conn := &passiveConn{closed: make(chan struct{})}
		term, err := terminal.New(terminal.Options{Conn: conn, OnExit: onExit})
		if err != nil {
			return nil, err
		}
		view := &controlView{}
		view.control = &fakeViewControl{view: view}
		return &pane{target: workspaceExecutor{id: id}, terminal: term, view: view, closeFn: func() { _ = term.Close() }}, nil
	})
	t.Cleanup(w.shutdown)
	a, err := w.NewTab(true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.SplitSession(a.SessionID, "right", true)
	if err != nil {
		t.Fatal(err)
	}
	confirmation := &panePaste{text: "first\n  second\n", ctx: context.Background(), answer: make(chan bool, 1)}
	w.panes[a.SessionID].paste = confirmation
	view := &workspaceView{workspace: w, update: func(fn func()) { fn() }}
	tester := ui.NewTester(view.view, 780, 480)
	tester.SetFocused(true)
	tester.Frame()
	if _, found := tester.Find("确认粘贴"); found {
		t.Fatal("其他窗格显示了确认入口")
	}
	if err := w.FocusSession(a.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if _, found := tester.Find("确认粘贴"); !found {
		t.Fatal("目标窗格没有确认入口")
	}
	if err := w.FocusSession(b.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if _, found := tester.Find("确认粘贴"); found {
		t.Fatal("切换后仍显示确认入口")
	}
	if err := w.FocusSession(a.SessionID); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if err := tester.Click("取消"); err != nil {
		t.Fatal(err)
	}
	select {
	case approved := <-confirmation.answer:
		if approved {
			t.Fatal("取消被当成批准")
		}
	default:
		t.Fatal("取消未响应")
	}
}
