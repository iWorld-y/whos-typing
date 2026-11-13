package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type workspaceStub struct {
	mu       sync.Mutex
	sessions map[string]Executor
	current  string
	tabTitle string
}

func (w *workspaceStub) Execute(context.Context, string) Result {
	return Result{Err: errors.New("不能直接执行")}
}
func (w *workspaceStub) ResolveSession(id string) (string, Executor, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if id == "" {
		if len(w.sessions) == 0 {
			return "", nil, errors.New("没有会话")
		}
		if len(w.sessions) != 1 {
			return "", nil, errors.New("多个会话，请指定 --session")
		}
		for key := range w.sessions {
			id = key
		}
	}
	e := w.sessions[id]
	if e == nil {
		return "", nil, errors.New("会话不存在")
	}
	return id, e, nil
}
func (w *workspaceStub) ListSessions() (SessionList, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l := SessionList{Sessions: []SessionInfo{}, SessionID: w.current}
	for id := range w.sessions {
		l.Sessions = append(l.Sessions, SessionInfo{SessionID: id, TabID: "tab", TabTitle: w.tabTitle, Title: "终端", Control: "human"})
	}
	return l, nil
}
func (w *workspaceStub) CurrentSession() (CurrentSession, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sessions[w.current] == nil {
		return CurrentSession{}, errors.New("没有当前会话")
	}
	return CurrentSession{SessionInfo: SessionInfo{SessionID: w.current, TabID: "tab", TabTitle: w.tabTitle, Title: "终端", Control: "human"}}, nil
}
func (w *workspaceStub) NewTab(bool) (CreatedSession, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessions["new"] = executorFunc(func(context.Context, string) Result { return Result{} })
	return CreatedSession{SessionID: "new", TabID: "new-tab"}, nil
}
func (w *workspaceStub) RenameTab(id, title string) error {
	if id != "tab" || strings.TrimSpace(title) == "" {
		return errors.New("标签页或名称无效")
	}
	w.mu.Lock()
	w.tabTitle = title
	w.mu.Unlock()
	return nil
}
func (w *workspaceStub) SplitSession(id, direction string, _ bool) (CreatedSession, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sessions[id] == nil {
		return CreatedSession{}, errors.New("会话不存在")
	}
	if direction != "right" && direction != "down" {
		return CreatedSession{}, errors.New("分屏方向无效")
	}
	w.sessions["split"] = executorFunc(func(context.Context, string) Result { return Result{} })
	return CreatedSession{SessionID: "split", TabID: "tab"}, nil
}
func (w *workspaceStub) FocusSession(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.current = id
	return nil
}

func TestWorkspaceHandshakeRejectsOldServerWithoutSendingCommand(t *testing.T) {
	dir := testDir(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath(dir), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	observed := make(chan []byte, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			observed <- nil
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		first, _ := reader.ReadBytes('\n')
		_ = json.NewEncoder(conn).Encode(response{Error: "命令请求格式错误"})
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		rest, _ := io.ReadAll(reader)
		observed <- append(first, rest...)
	}()
	if _, err := runSession(socketPath(dir), "target", "sensitive-command", 0, io.Discard); err == nil || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("旧服务未拒绝: %v", err)
	}
	got := <-observed
	if strings.Contains(string(got), "sensitive-command") || strings.Contains(string(got), "target") || !strings.Contains(string(got), "hello") {
		t.Fatalf("握手前发送了目标或命令: %s", got)
	}
}

func TestWorkspaceLegacyRoutingAndExplicitTarget(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	makeTarget := func(id string) Executor {
		return executorFunc(func(_ context.Context, cmd string) Result {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, id+":"+cmd)
			return Result{}
		})
	}
	w := &workspaceStub{sessions: map[string]Executor{"a": makeTarget("a")}, current: "a"}
	path := readTestServer(t, w)
	if _, err := run(path, "old", io.Discard); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.sessions["b"] = makeTarget("b")
	w.mu.Unlock()
	if _, err := run(path, "must-not-run", io.Discard); err == nil || !strings.Contains(err.Error(), "指定") {
		t.Fatalf("旧 CLI 多会话未拒绝: %v", err)
	}
	if _, err := runSession(path, "missing", "must-not-run", 0, io.Discard); err == nil {
		t.Fatal("无效 ID 未拒绝")
	}
	if _, err := runSession(path, "b", "new", 0, io.Discard); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(calls, ",") != "a:old,b:new" {
		t.Fatalf("路由错误: %v", calls)
	}
}

func TestWorkspaceRequestRemainsBoundWhileFocusChanges(t *testing.T) {
	started, proceed := make(chan struct{}), make(chan struct{})
	a := executorFunc(func(_ context.Context, cmd string) Result {
		if cmd != "long" {
			t.Errorf("命令错误: %s", cmd)
		}
		close(started)
		<-proceed
		return Result{ExitCode: 7}
	})
	b := executorFunc(func(context.Context, string) Result { t.Error("错误地调用了 B"); return Result{} })
	w := &workspaceStub{sessions: map[string]Executor{"a": a, "b": b}, current: "a"}
	path := readTestServer(t, w)
	done := make(chan int, 1)
	go func() {
		code, err := runSession(path, "a", "long", 0, io.Discard)
		if err != nil {
			t.Error(err)
		}
		done <- code
	}()
	<-started
	if _, err := workspaceCall(path, request{Action: "session.focus", SessionID: "b"}); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	if code := <-done; code != 7 {
		t.Fatalf("原会话结果错误: %d", code)
	}
	var screen bytes.Buffer
	if err := readSessionFor(path, "missing", &screen); err == nil {
		t.Fatal("未知 ID 应被拒绝")
	}
}

func TestWorkspaceDiscoveryAndCreate(t *testing.T) {
	w := &workspaceStub{sessions: map[string]Executor{}}
	path := readTestServer(t, w)
	if l, err := listWorkspace(path); err != nil || len(l.Sessions) != 0 {
		t.Fatalf("空列表错误: %+v %v", l, err)
	}
	if _, err := currentWorkspace(path); err == nil {
		t.Fatal("空工作区不应有当前会话")
	}
	created, err := createWorkspace(path, request{Action: "tab.new"})
	if err != nil || created.SessionID != "new" {
		t.Fatalf("创建错误: %+v %v", created, err)
	}
	l, err := listWorkspace(path)
	if err != nil || len(l.Sessions) != 1 || l.Sessions[0].SessionID != "new" {
		t.Fatalf("列表无法核对创建结果: %+v %v", l, err)
	}
}

type routedTarget struct {
	mu                    sync.Mutex
	id, owner             string
	manual, auto, release int
}

func (p *routedTarget) Execute(context.Context, string) Result { return Result{} }
func (p *routedTarget) ReadSession() (string, error)           { return "screen-" + p.id, nil }
func (p *routedTarget) ControlStatus() string                  { p.mu.Lock(); defer p.mu.Unlock(); return p.owner }
func (p *routedTarget) ReleaseControl() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.release++
	p.owner = "human"
	return nil
}
func (p *routedTarget) Attach(_ context.Context, _ string, show func(string) error) error {
	p.mu.Lock()
	p.manual++
	p.mu.Unlock()
	return show("bootstrap-" + p.id)
}
func (p *routedTarget) AttachAuto(_ context.Context, _ string, notify func() error) error {
	p.mu.Lock()
	p.auto++
	p.mu.Unlock()
	return notify()
}

func TestWorkspaceRoutesReadControlAndAttachToBoundTargets(t *testing.T) {
	a := &routedTarget{id: "a", owner: "human_takeover"}
	b := &routedTarget{id: "b", owner: "agent"}
	w := &workspaceStub{sessions: map[string]Executor{"a": a, "b": b}, current: "a"}
	path := readTestServer(t, w)
	var output bytes.Buffer
	if err := readSessionFor(path, "a", &output); err != nil || output.String() != "screen-a" {
		t.Fatalf("A 快照错误: %q %v", output.String(), err)
	}
	output.Reset()
	if err := readSessionFor(path, "b", &output); err != nil || output.String() != "screen-b" {
		t.Fatalf("B 快照错误: %q %v", output.String(), err)
	}
	r, err := workspaceCall(path, request{Action: "session.status", SessionID: "a"})
	if err != nil || r.Control != "human_takeover" {
		t.Fatalf("A 控制状态错误: %+v %v", r, err)
	}
	if _, err := workspaceCall(path, request{Action: "session.release", SessionID: "b"}); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := attachSession(path, "a", "bash", false, &output); err != nil || !strings.Contains(output.String(), "bootstrap-a") {
		t.Fatalf("手动接入错误: %q %v", output.String(), err)
	}
	output.Reset()
	if err := attachSession(path, "b", "bash", true, &output); err != nil || !strings.Contains(output.String(), "远端 Bash 已接入") {
		t.Fatalf("自动接入错误: %q %v", output.String(), err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if a.owner != "human_takeover" || a.manual != 1 || a.auto != 0 || a.release != 0 || b.owner != "human" || b.manual != 0 || b.auto != 1 || b.release != 1 {
		t.Fatalf("操作串到其他会话: A=%+v B=%+v", a, b)
	}
}

func TestWorkspaceImplicitRequestStaysBoundAfterSessionAdded(t *testing.T) {
	started, proceed := make(chan struct{}), make(chan struct{})
	a := executorFunc(func(context.Context, string) Result { close(started); <-proceed; return Result{ExitCode: 9} })
	b := executorFunc(func(context.Context, string) Result { t.Error("无目标请求被重定向到 B"); return Result{} })
	w := &workspaceStub{sessions: map[string]Executor{"a": a}, current: "a"}
	path := readTestServer(t, w)
	done := make(chan int, 1)
	go func() {
		code, err := runSession(path, "", "bound", 0, io.Discard)
		if err != nil {
			t.Error(err)
		}
		done <- code
	}()
	<-started
	w.mu.Lock()
	w.sessions["b"] = b
	w.current = "b"
	w.mu.Unlock()
	close(proceed)
	if code := <-done; code != 9 {
		t.Fatalf("绑定请求返回错误结果: %d", code)
	}
}

func TestWorkspaceBoundTargetCloseDoesNotRunElsewhere(t *testing.T) {
	started, closed := make(chan struct{}), make(chan struct{})
	a := executorFunc(func(context.Context, string) Result {
		close(started)
		<-closed
		return Result{Err: errors.New("会话已结束，结果未知")}
	})
	b := executorFunc(func(context.Context, string) Result { t.Error("目标关闭后不能重定向"); return Result{} })
	w := &workspaceStub{sessions: map[string]Executor{"a": a, "b": b}, current: "a"}
	path := readTestServer(t, w)
	done := make(chan error, 1)
	go func() { _, err := runSession(path, "a", "run", 0, io.Discard); done <- err }()
	<-started
	w.mu.Lock()
	delete(w.sessions, "a")
	w.current = "b"
	w.mu.Unlock()
	close(closed)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "结果未知") {
		t.Fatalf("目标关闭未报告结果未知: %v", err)
	}
}

func TestWorkspaceLostCreationReplyCanBeCheckedByList(t *testing.T) {
	w := &workspaceStub{sessions: map[string]Executor{}}
	path := readTestServer(t, w)
	c, err := connectWorkspace(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.send(request{Action: "tab.new"}); err != nil {
		t.Fatal(err)
	}
	_ = c.conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		l, err := listWorkspace(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Sessions) == 1 && l.Sessions[0].SessionID == "new" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("无法通过列表核对已创建会话")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWorkspaceRenameTabProtocol(t *testing.T) {
	w := &workspaceStub{sessions: map[string]Executor{"a": executorFunc(func(context.Context, string) Result { return Result{} })}, current: "a", tabTitle: "01"}
	path := readTestServer(t, w)
	if _, err := workspaceCall(path, request{Action: "tab.rename", TabID: "tab", Title: "构建"}); err != nil {
		t.Fatal(err)
	}
	current, err := currentWorkspace(path)
	if err != nil || current.TabTitle != "构建" || current.SessionID != "a" {
		t.Fatalf("重命名结果错误: %+v %v", current, err)
	}
	list, err := listWorkspace(path)
	if err != nil || list.Sessions[0].TabTitle != "构建" {
		t.Fatalf("列表标签名称错误: %+v %v", list, err)
	}
	for _, req := range []request{{Action: "tab.rename", TabID: "missing", Title: "其他"}, {Action: "tab.rename", TabID: "tab", Title: " \t"}} {
		if _, err := workspaceCall(path, req); err == nil {
			t.Fatalf("无效重命名未拒绝: %+v", req)
		}
	}
	current, _ = currentWorkspace(path)
	if current.TabTitle != "构建" {
		t.Fatal("无效请求更改了名称")
	}
}

func TestWorkspaceOpaqueLongAndShortIDs(t *testing.T) {
	const oldID = "1234567890abcdef1234567890abcdef"
	const shortID = "session_02"
	var mu sync.Mutex
	var calls []string
	makeTarget := func(id string) Executor {
		return executorFunc(func(_ context.Context, command string) Result {
			mu.Lock()
			calls = append(calls, id+":"+command)
			mu.Unlock()
			return Result{}
		})
	}
	w := &workspaceStub{sessions: map[string]Executor{oldID: makeTarget(oldID), shortID: makeTarget(shortID)}, current: oldID}
	path := readTestServer(t, w)
	list, err := listWorkspace(path)
	if err != nil {
		t.Fatal(err)
	}
	foundOld, foundNew := false, false
	for _, item := range list.Sessions {
		foundOld = foundOld || item.SessionID == oldID
		foundNew = foundNew || item.SessionID == shortID
	}
	if !foundOld || !foundNew {
		t.Fatalf("列表未保留完整标识: %+v", list)
	}
	if _, err := runSession(path, oldID, "new-client", 0, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := runSession(path, shortID, "new-format", 0, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "session_0", "session_2", "01", "tab_02", "0000002a", oldID[:8], "2"} {
		if _, err := runSession(path, bad, "must-not-run", 0, io.Discard); err == nil {
			t.Fatalf("接受不完整目标 %q", bad)
		}
	}
	// 模拟旧版多会话 CLI 的 v2 握手与请求，不预处理新服务的短 ID。
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	if err := enc.Encode(map[string]any{"action": "hello"}); err != nil {
		t.Fatal(err)
	}
	var hello response
	if err := dec.Decode(&hello); err != nil || hello.Version != 2 || !hello.MultiSession {
		t.Fatalf("握手错误: %+v %v", hello, err)
	}
	if err := enc.Encode(map[string]any{"session_id": shortID, "command": "old-client"}); err != nil {
		t.Fatal(err)
	}
	var reply response
	if err := dec.Decode(&reply); err != nil || reply.Error != "" {
		t.Fatalf("旧客户端请求失败: %+v %v", reply, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(calls, ",") != oldID+":new-client,"+shortID+":new-format,"+shortID+":old-client" {
		t.Fatalf("目标未原样传递: %v", calls)
	}
}

func TestStatusSessionJSONLegacyDiagnostics(t *testing.T) {
	a := &routedTarget{id: "a", owner: "human_takeover"}
	w := &workspaceStub{sessions: map[string]Executor{"a": a}, current: "a"}
	path := readTestServer(t, w)
	var out bytes.Buffer
	if err := statusSessionJSON(path, "a", &out); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["control"] != "human_takeover" || got["diagnostics_available"] != false || got["message"] == "" {
		t.Fatalf("旧窗口状态: %v", got)
	}
	if a.ControlStatus() != "human_takeover" {
		t.Fatal("查询改变控制方")
	}
}

type diagnosticTarget struct{ *routedTarget }

func (p *diagnosticTarget) SessionDiagnostic() Diagnostic {
	return Diagnostic{Control: p.ControlStatus(), Remote: "not_ready", Unknown: true, SuggestedAction: "接管终端并使用恢复远端接入", Available: true}
}

func TestStatusSessionJSONDiagnostics(t *testing.T) {
	p := &diagnosticTarget{&routedTarget{id: "a", owner: "agent"}}
	w := &workspaceStub{sessions: map[string]Executor{"a": p}, current: "a"}
	path := readTestServer(t, w)
	var out bytes.Buffer
	if err := statusSessionJSON(path, "a", &out); err != nil {
		t.Fatal(err)
	}
	var got Diagnostic
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Control != "agent" || got.Remote != "not_ready" || !got.Unknown || !got.Available || got.SuggestedAction == "" {
		t.Fatalf("诊断内容错误: %+v", got)
	}
	if p.ControlStatus() != "agent" {
		t.Fatal("查询改变控制方")
	}
}
