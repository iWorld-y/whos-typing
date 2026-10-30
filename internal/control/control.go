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
	Command string `json:"command"`
	Action  string `json:"action,omitempty"`
	Shell   string `json:"shell,omitempty"`
	Timeout int64  `json:"timeout,omitempty"`
}

type response struct {
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
	Size     int64  `json:"size"`
	Phase    string `json:"phase,omitempty"`
	Script   string `json:"script,omitempty"`
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

// SessionReader 返回窗口当前显示及滚动历史中的文本。
type SessionReader interface {
	ReadSession() (string, error)
}

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

// AttachRemote 显示一次性接入命令并等待用户在远端执行。
func AttachRemote(shellName string, out io.Writer) error {
	return attachRemote(socketPath(directory()), shellName, out)
}

func attachRemote(path, shellName string, out io.Writer) error {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return fmt.Errorf("无法连接 Who's Typing 窗口: %w", err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request{Action: "attach", Shell: shellName}); err != nil {
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
			return errors.New(reply.Error)
		}
		switch reply.Phase {
		case "instructions":
			if _, err := fmt.Fprintf(out, "请在 GUI 的远端 Bash 空提示符粘贴并执行以下一行：\n%s\n等待接入，按 Ctrl+C 可取消。\n", reply.Script); err != nil {
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
