// Package control 提供仅供当前用户访问的本地命令入口。
package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type request struct {
	Command   string `json:"command"`
	Action    string `json:"action,omitempty"`
	Shell     string `json:"shell,omitempty"`
	Timeout   int64  `json:"timeout,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TabID     string `json:"tab_id,omitempty"`
	Title     string `json:"title,omitempty"`
	Direction string `json:"direction,omitempty"`
	Focus     bool   `json:"focus,omitempty"`
}

type response struct {
	ExitCode     int             `json:"exit_code"`
	Error        string          `json:"error,omitempty"`
	Size         int64           `json:"size"`
	Phase        string          `json:"phase,omitempty"`
	Script       string          `json:"script,omitempty"`
	Control      string          `json:"control,omitempty"`
	Diagnostic   *Diagnostic     `json:"diagnostic,omitempty"`
	Version      int             `json:"version,omitempty"`
	MultiSession bool            `json:"multi_session,omitempty"`
	List         *SessionList    `json:"list,omitempty"`
	Current      *CurrentSession `json:"current,omitempty"`
	Created      *CreatedSession `json:"created,omitempty"`
}

// Result 包含终端原始输出、退出码及无法完成时的错误。
type Result struct {
	Output   *os.File
	ExitCode int
	Err      error
}

// Executor 在图形窗口所持有的 Shell 中执行命令。
type Executor interface {
	Execute(context.Context, string) Result
}

// Attacher 等待用户在远端 Shell 手动完成接入。
type Attacher interface {
	Attach(context.Context, string, func(string) error) error
}

// AutoAttacher 等待用户在窗口确认后自动接入远端 Shell。
type AutoAttacher interface {
	AttachAuto(context.Context, string, func() error) error
}

// SessionReader 返回窗口当前显示及滚动历史中的文本。
type SessionReader interface {
	ReadSession() (string, error)
}

// ControlManager 读取和主动交还当前终端控制权。
type ControlManager interface {
	ControlStatus() string
	ReleaseControl() error
}

// Diagnostic 描述只读终端状态，Available 为 false 表示旧窗口未提供诊断。
type Diagnostic struct {
	Control         string `json:"control"`
	Remote          string `json:"remote"`
	Unknown         bool   `json:"unknown_result"`
	SuggestedAction string `json:"suggested_action"`
	Available       bool   `json:"diagnostics_available"`
}

type diagnosticProvider interface{ SessionDiagnostic() Diagnostic }

// Server 管理当前用户的 Unix socket 和相关连接。
type Server struct {
	dir      string
	listener *net.UnixListener
	lock     *os.File
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[*net.UnixConn]struct{}
	serveEnd chan struct{}
}

func directory() string {
	if dir := testDirectory(); dir != "" {
		return dir
	}
	return filepath.Join("/tmp", "whos-typing-"+strconv.Itoa(os.Geteuid()))
}

func socketPath(dir string) string { return filepath.Join(dir, "control.sock") }

// Listen 创建私有目录并独占监听，避免覆盖另一个运行中的窗口。
func Listen() (*Server, error) {
	return listen(directory())
}

func listen(dir string) (*Server, error) {
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("创建本地通信目录失败: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || int(stat.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("本地通信目录的所有者或权限不安全: %s", dir)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("已有 Who's Typing 窗口运行")
	}
	path := socketPath(dir)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			_ = lock.Close()
			return nil, fmt.Errorf("本地通信路径不是 socket: %s", path)
		}
		if err := os.Remove(path); err != nil {
			_ = lock.Close()
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = lock.Close()
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("监听本地 socket 失败: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		_ = lock.Close()
		return nil, err
	}
	return &Server{dir: dir, listener: listener, lock: lock, conns: make(map[*net.UnixConn]struct{}), serveEnd: make(chan struct{})}, nil
}

// Dir 返回仅当前用户可访问的运行目录。
func (s *Server) Dir() string { return s.dir }

// Serve 接收命令，直到监听关闭。
func (s *Server) Serve(executor Executor) {
	defer close(s.serveEnd)
	for {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			defer func() {
				s.mu.Lock()
				delete(s.conns, conn)
				s.mu.Unlock()
			}()
			handle(conn, executor)
		}()
	}
}

func handle(conn *net.UnixConn, executor Executor) {
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		_ = json.NewEncoder(conn).Encode(response{Error: "命令请求格式错误"})
		return
	}
	if executor == nil {
		_ = json.NewEncoder(conn).Encode(response{Error: "终端未启动"})
		return
	}
	workspace, hasWorkspace := executor.(Workspace)
	if req.Action == "hello" {
		if !hasWorkspace {
			_ = json.NewEncoder(conn).Encode(response{Error: "窗口不支持多会话"})
			return
		}
		if err := json.NewEncoder(conn).Encode(response{Version: protocolVersion, MultiSession: true}); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err = r.ReadBytes('\n')
		_ = conn.SetReadDeadline(time.Time{})
		req = request{}
		if err != nil || json.Unmarshal(line, &req) != nil {
			_ = json.NewEncoder(conn).Encode(response{Error: "命令请求格式错误"})
			return
		}
	} else if req.SessionID != "" {
		_ = json.NewEncoder(conn).Encode(response{Error: "请先确认多会话协议"})
		return
	}
	if hasWorkspace {
		reply := response{}
		switch req.Action {
		case "session.list":
			list, err := workspace.ListSessions()
			if err != nil {
				reply.Error = err.Error()
			} else {
				reply.List = &list
			}
		case "session.current":
			current, err := workspace.CurrentSession()
			if err != nil {
				reply.Error = err.Error()
			} else {
				reply.Current = &current
			}
		case "tab.rename":
			if err := workspace.RenameTab(req.TabID, req.Title); err != nil {
				reply.Error = err.Error()
			}
		case "tab.new":
			created, err := workspace.NewTab(req.Focus)
			if err != nil {
				reply.Error = err.Error()
			} else {
				reply.Created = &created
			}
		default:
			id, target, err := workspace.ResolveSession(req.SessionID)
			if err != nil {
				reply.Error = err.Error()
				_ = json.NewEncoder(conn).Encode(reply)
				return
			}
			switch req.Action {
			case "session.split":
				created, err := workspace.SplitSession(id, req.Direction, req.Focus)
				if err != nil {
					reply.Error = err.Error()
				} else {
					reply.Created = &created
				}
			case "session.focus":
				if err := workspace.FocusSession(id); err != nil {
					reply.Error = err.Error()
				}
			default:
				executor = target
				goto bound
			}
		}
		_ = json.NewEncoder(conn).Encode(reply)
		return
	}
bound:
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, r)
		cancel()
	}()
	if req.Action == "session.read" {
		handleSessionRead(conn, executor)
		return
	}
	if req.Action == "session.status" || req.Action == "session.release" {
		manager, ok := executor.(ControlManager)
		if !ok {
			_ = json.NewEncoder(conn).Encode(response{Error: "终端不支持控制权管理"})
			return
		}
		reply := response{}
		if req.Action == "session.release" {
			if err := manager.ReleaseControl(); err != nil {
				reply.Error = err.Error()
			}
		} else {
			reply.Control = manager.ControlStatus()
			if provider, ok := executor.(diagnosticProvider); ok {
				d := provider.SessionDiagnostic()
				reply.Control = d.Control
				reply.Diagnostic = &d
			}
		}
		_ = json.NewEncoder(conn).Encode(reply)
		return
	}
	if req.Action == "attach" {
		attacher, ok := executor.(Attacher)
		if !ok {
			_ = json.NewEncoder(conn).Encode(response{Error: "终端不支持远端接入"})
			return
		}
		err := attacher.Attach(ctx, req.Shell, func(script string) error {
			return json.NewEncoder(conn).Encode(response{Phase: "instructions", Script: script})
		})
		reply := response{Phase: "attached"}
		if err != nil {
			reply.Error = err.Error()
		}
		_ = json.NewEncoder(conn).Encode(reply)
		return
	}
	if req.Action == "attach.auto" {
		attacher, ok := executor.(AutoAttacher)
		if !ok {
			_ = json.NewEncoder(conn).Encode(response{Error: "终端不支持自动接入"})
			return
		}
		err := attacher.AttachAuto(ctx, req.Shell, func() error {
			return json.NewEncoder(conn).Encode(response{Phase: "confirmation"})
		})
		reply := response{Phase: "attached"}
		if err != nil {
			reply.Error = err.Error()
		}
		_ = json.NewEncoder(conn).Encode(reply)
		return
	}
	if req.Action != "" || req.Timeout < 0 {
		_ = json.NewEncoder(conn).Encode(response{Error: "命令请求格式错误"})
		return
	}
	if req.Timeout > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, time.Duration(req.Timeout))
		defer stop()
	}
	result := executor.Execute(ctx, req.Command)
	if result.Output != nil {
		defer os.Remove(result.Output.Name())
		defer result.Output.Close()
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	reply := response{ExitCode: result.ExitCode}
	if result.Err != nil {
		reply.Error = result.Err.Error()
	}
	if result.Output != nil {
		info, err := result.Output.Stat()
		if err != nil {
			reply.Error = err.Error()
		} else {
			reply.Size = info.Size()
		}
	}
	if err := json.NewEncoder(conn).Encode(reply); err != nil {
		return
	}
	if result.Output != nil {
		_, _ = io.CopyN(conn, result.Output, reply.Size)
	}
}

// Close 停止接收请求并等待现有请求结束。
func (s *Server) Close() error {
	err := s.listener.Close()
	<-s.serveEnd
	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	return errors.Join(err, s.lock.Close())
}

// Run 将命令发送到已运行的 GUI，并把终端原始输出写入 out。
func Run(command string, out io.Writer) (int, error) {
	return run(socketPath(directory()), command, out)
}

// RunWithTimeout 设置执行时间上限；超时不代表远端命令已停止。
func RunWithTimeout(command string, timeout time.Duration, out io.Writer) (int, error) {
	return runTimed(socketPath(directory()), command, timeout, out)
}

func run(path, command string, out io.Writer) (int, error) {
	return runTimed(path, command, 0, out)
}

func runTimed(path, command string, timeout time.Duration, out io.Writer) (int, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return 1, fmt.Errorf("无法连接 Who's Typing 窗口: %w", err)
	}
	defer conn.Close()
	if timeout > 0 {
		if err := conn.SetDeadline(time.Now().Add(timeout).Add(500 * time.Millisecond)); err != nil {
			return 1, err
		}
	}
	if err := json.NewEncoder(conn).Encode(request{Command: command, Timeout: int64(timeout)}); err != nil {
		if timeoutError(err) {
			return 1, errors.New("执行超时，结果未知，命令可能仍在执行")
		}
		return 1, fmt.Errorf("发送命令失败: %w", err)
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		if timeoutError(err) {
			return 1, errors.New("执行超时，结果未知，命令可能仍在执行")
		}
		return 1, fmt.Errorf("终端连接已断开: %w", err)
	}
	var reply response
	if err := json.Unmarshal(line, &reply); err != nil || reply.Size < 0 {
		return 1, errors.New("终端响应格式错误")
	}
	if _, err := io.CopyN(out, r, reply.Size); err != nil {
		if timeoutError(err) {
			return 1, errors.New("执行超时，结果未知，命令可能仍在执行")
		}
		return 1, fmt.Errorf("读取命令输出失败: %w", err)
	}
	if reply.Error != "" {
		return 1, errors.New(reply.Error)
	}
	return reply.ExitCode, nil
}

func timeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// SessionStatus 输出当前终端控制方。
func SessionStatus(out io.Writer) error {
	return sessionStatus(socketPath(directory()), out)
}

func sessionStatus(path string, out io.Writer) error {
	reply, err := sessionControl(path, "session.status")
	if err != nil {
		return err
	}
	switch reply.Control {
	case "human", "agent", "human_takeover":
		_, err = fmt.Fprintln(out, reply.Control)
		return err
	default:
		return errors.New("终端控制权响应格式错误")
	}
}

// ReleaseSession 主动交还 Agent 对终端的控制权。
func ReleaseSession() error {
	return sessionRelease(socketPath(directory()))
}

func sessionRelease(path string) error {
	_, err := sessionControl(path, "session.release")
	return err
}

func sessionControl(path, action string) (response, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return response{}, fmt.Errorf("无法连接 Who's Typing 窗口: %w", err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request{Action: action}); err != nil {
		return response{}, fmt.Errorf("发送控制权请求失败: %w", err)
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return response{}, fmt.Errorf("终端连接已断开: %w", err)
	}
	var reply response
	if err := json.Unmarshal(line, &reply); err != nil {
		return response{}, errors.New("终端响应格式错误")
	}
	if reply.Error != "" {
		return response{}, errors.New(reply.Error)
	}
	return reply, nil
}

// AttachRemote 显示一次性接入命令并等待用户在远端执行。
func AttachRemote(shellName string, out io.Writer) error {
	return attachRemote(socketPath(directory()), shellName, out)
}

func attachRemote(path, shellName string, out io.Writer) error {
	return attachRemoteAction(path, shellName, "attach", out)
}

// AttachRemoteAuto 等待用户在窗口确认，并由窗口自动输入接入命令。
func AttachRemoteAuto(shellName string, out io.Writer) error {
	return attachRemoteAuto(socketPath(directory()), shellName, out)
}

func attachRemoteAuto(path, shellName string, out io.Writer) error {
	return attachRemoteAction(path, shellName, "attach.auto", out)
}

func attachRemoteAction(path, shellName, action string, out io.Writer) error {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return fmt.Errorf("无法连接 Who's Typing 窗口: %w", err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request{Action: action, Shell: shellName}); err != nil {
		return fmt.Errorf("发送接入请求失败: %w", err)
	}
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("终端连接已断开: %w", err)
		}
		var reply response
		if err := json.Unmarshal(line, &reply); err != nil {
			return errors.New("终端响应格式错误")
		}
		if reply.Error != "" {
			if action == "attach.auto" && reply.Error == "命令请求格式错误" {
				return errors.New("当前 Who's Typing 窗口不支持自动接入，请使用相同版本的窗口和 CLI")
			}
			return errors.New(reply.Error)
		}
		switch reply.Phase {
		case "instructions":
			if action != "attach" {
				return errors.New("终端不支持自动接入")
			}
			if _, err := fmt.Fprintf(out, "请在 GUI 的远端 Bash 空提示符粘贴并执行以下一行：\n%s\n等待接入，按 Ctrl+C 可取消。\n", reply.Script); err != nil {
				return err
			}
		case "confirmation":
			if action != "attach.auto" {
				return errors.New("终端响应格式错误")
			}
			if _, err := fmt.Fprintln(out, "请在 Who's Typing 窗口确认当前为目标服务器的远端 Bash 空提示符。\n等待确认和接入，按 Ctrl+C 可取消。"); err != nil {
				return err
			}
		case "attached":
			_, err := fmt.Fprintln(out, "远端 Bash 已接入。")
			return err
		default:
			return errors.New("终端响应格式错误")
		}
	}
}
