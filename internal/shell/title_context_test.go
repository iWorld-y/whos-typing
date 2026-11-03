package shell

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func titleMark(user, host, dir string) string {
	encode := base64.StdEncoding.EncodeToString
	return "M:" + encode([]byte(user)) + ":" + encode([]byte(host)) + ":" + encode([]byte(dir))
}

func TestTitleContextFrames(t *testing.T) {
	s := &Session{initSeen: true, initial: true, marker: []byte("\x1eWT:test:"), titleContext: TitleContext{User: "old", Host: "old", Directory: "/old"}}
	mark := titleMark("用户", "host", "/a:b\n表情😀")
	data := "before\x1eWT:test:" + mark + "\x1f\x1eWT:test:P\x1fafter"
	for _, b := range []byte(data) {
		s.readBuf = append(s.readBuf, b)
		s.parse(false)
	}
	if got := s.TitleContext(); got.User != "用户" || got.Host != "host" || got.Directory != "/a:b\n表情😀" {
		t.Fatalf("上下文错误: %+v", got)
	}
	if string(s.parsed) != "beforeafter" {
		t.Fatalf("标记进入输出: %q", s.parsed)
	}
	for _, invalid := range []string{"M:!!:YQ==:YQ==", "M:YQ==:YQ==:", "M:YQ==:YQ==:" + strings.Repeat("A", 9000)} {
		data := "\x1eWT:test:" + invalid + "\x1f\x1eWT:test:P\x1f"
		for _, b := range []byte(data) {
			s.readBuf = append(s.readBuf, b)
			s.parse(false)
		}
		if got := s.TitleContext(); got.Directory != "/a:b\n表情😀" {
			t.Fatalf("异常元信息覆盖了上下文: %+v", got)
		}
	}
	if string(s.parsed) != "beforeafter" {
		t.Fatalf("异常标记进入输出: %q", s.parsed)
	}
}

func TestTitleContextRealShells(t *testing.T) {
	for _, path := range []string{"/bin/zsh", "/bin/bash", "/opt/homebrew/bin/fish"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := os.Stat(path); err != nil {
				t.Skipf("Shell 不可用: %v", err)
			}
			f := newFixture(t, path)
			dir := filepath.Join(t.TempDir(), "目录:😀\n下一行")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			output, code, err := f.run(t, "cd "+f.s.quote(dir))
			if err != nil || code != 0 || output != "" {
				t.Fatalf("cd: %q %d %v", output, code, err)
			}
			if got := f.s.TitleContext(); got.Directory != dir || got.User == "" || got.Host == "" {
				t.Fatalf("标题上下文: %+v, 预期目录 %q", got, dir)
			}
			if strings.Contains(f.screen(), string(f.s.marker)) {
				t.Fatalf("元信息进入屏幕: %q", f.screen())
			}
			if output, code, err := f.run(t, "false"); err != nil || code != 1 || output != "" {
				t.Fatalf("退出状态: %q %d %v", output, code, err)
			}
		})
	}
}

func TestRemoteTitleRequiresConfirmedPrompt(t *testing.T) {
	s := &Session{initSeen: true, initial: true, marker: []byte("\x1eWT:local:"), titleContext: TitleContext{User: "local", Host: "host", Directory: "/local"}}
	r := &remoteSession{marker: []byte("\x1eWT:remote:"), attaching: true, installed: true, bootstrap: "setup", input: []byte("setup\r"), attachDone: make(chan error, 1)}
	s.remote = r
	if !s.controlRemote(r, titleMark("remote", "server", "/first")) {
		t.Fatal("未识别远端元信息")
	}
	if got := s.TitleContext(); got.User != "local" {
		t.Fatalf("确认前改变标题: %+v", got)
	}
	s.controlRemote(r, "C:1")
	s.controlRemote(r, "P:1")
	if got := s.TitleContext(); got.User != "remote" || got.Directory != "/first" {
		t.Fatalf("远端标题: %+v", got)
	}
	s.controlRemote(r, "F")
	if got := s.TitleContext(); !got.Unknown || got.Directory != "/first" {
		t.Fatalf("远端未知时未保留上下文: %+v", got)
	}
	s.controlRemote(r, titleMark("remote", "server", "/next"))
	s.controlRemote(r, "C:2")
	s.controlRemote(r, "P:2")
	if got := s.TitleContext(); got.Directory != "/next" || got.Unknown {
		t.Fatalf("远端目录未更新: %+v", got)
	}
	s.control(titleMark("local", "host", "/back"))
	s.control("P")
	if got := s.TitleContext(); got.User != "local" || got.Directory != "/back" {
		t.Fatalf("本地上下文未恢复: %+v", got)
	}
}

func TestTruncatedTitleFrameRecoversCommandBoundaries(t *testing.T) {
	for _, length := range []int{12, maxTitleMarker + 20} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			output, err := os.CreateTemp(t.TempDir(), "output-")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			req := &request{output: output, done: make(chan struct{})}
			s := &Session{initSeen: true, initial: true, marker: []byte("\x1eWT:test:"), active: req}
			data := "\x1eWT:test:S\x1fbefore\x1eWT:test:M:" + strings.Repeat("a", length) + "\x1eWT:test:E:7\x1f\x1eWT:test:P\x1fafter"
			for _, b := range []byte(data) {
				s.readBuf = append(s.readBuf, b)
				s.parse(false)
			}
			if s.active != nil || req.exitCode != 7 || !req.finished || !strings.HasSuffix(string(s.parsed), "beforeafter") {
				t.Fatalf("未恢复完成边界: parsed=%q req=%+v", s.parsed, req)
			}
			if _, err := output.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(output)
			if err != nil || string(got) != "before" {
				t.Fatalf("命令输出错误: %q %v", got, err)
			}
		})
	}
}

func TestTitleMetadataDoesNotInjectAtPasswordPrompt(t *testing.T) {
	sends := 0
	s := &Session{initSeen: true, marker: []byte("\x1eWT:local:"), titleContext: TitleContext{User: "local", Host: "host", Directory: "/home"}, send: func([]byte) { sends++ }}
	s.emit([]byte("Password: "))
	s.control("M:invalid:fields:")
	_ = s.TitleContext()
	if sends != 0 || string(s.parsed) != "Password: " || s.TitleContext().Directory != "/home" {
		t.Fatalf("未接入时产生输入或改变上下文: sends=%d output=%q", sends, s.parsed)
	}
}

func TestSearchDoesNotChangeRealShellControlOrIdle(t *testing.T) {
	f := newFixture(t, "/opt/homebrew/bin/fish")
	tester := ui.NewTester(func(c *ui.Context) { terminal.View(c, f.term).Fill().AutoFocus() }, 640, 400)
	tester.SetFocused(true)
	tester.Frame()
	done := make(chan error, 1)
	go func() {
		_, code, err := f.run(t, "printf 'needle one\\n'; sleep 1; printf 'needle two\\n'")
		if err == nil && code != 0 {
			err = fmt.Errorf("命令退出码 %d", code)
		}
		done <- err
	}()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		f.s.mu.Lock()
		active := f.s.active != nil && f.s.active.started
		f.s.mu.Unlock()
		if active {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.s.mu.Lock()
	if f.s.active == nil || !f.s.active.started {
		f.s.mu.Unlock()
		t.Fatal("命令未进入运行状态")
	}
	owner, idleStart, idleVersion, keys, dirty := f.s.owner, f.s.idleStarted, f.s.idleVersion, f.s.keys, f.s.dirty
	f.s.mu.Unlock()
	f.term.Search("needle")
	for range 5 {
		tester.Frame()
	}
	f.term.SearchNext()
	f.term.SearchPrevious()
	f.term.EndSearch()
	tester.Frame()
	f.s.mu.Lock()
	if f.s.active == nil || f.s.owner != owner || f.s.idleStarted != idleStart || f.s.idleVersion != idleVersion || f.s.keys != keys || f.s.dirty != dirty {
		f.s.mu.Unlock()
		t.Fatal("运行期间搜索改变控制权、计时或输入状态")
	}
	f.s.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.waitReady(t)
	f.s.mu.Lock()
	owner, idleStart, idleVersion, keys, dirty = f.s.owner, f.s.idleStarted, f.s.idleVersion, f.s.keys, f.s.dirty
	f.s.mu.Unlock()
	if owner != ControlAgent || idleStart.IsZero() {
		t.Fatal("命令结束后 Agent 空闲计时未启动")
	}
	f.term.Search("needle")
	tester.Frame()
	f.term.SearchNext()
	f.term.EndSearch()
	tester.Frame()
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if f.s.owner != owner || f.s.idleStarted != idleStart || f.s.idleVersion != idleVersion || f.s.keys != keys || f.s.dirty != dirty {
		t.Fatal("搜索重置了 Agent 控制权、空闲计时或输入状态")
	}
}
