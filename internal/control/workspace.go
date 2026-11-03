// 本文件定义多会话工作区协议及新版 CLI 客户端。
package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
	"unicode/utf8"
)

// SessionInfo 是可供 CLI 寻址的会话信息。
type SessionInfo struct {
	SessionID string `json:"session_id"`
	TabID     string `json:"tab_id"`
	TabTitle  string `json:"tab_title"`
	Title     string `json:"title"`
	Control   string `json:"control"`
}

// SessionList 包含存活会话、最后选中的会话及窗口前台状态。
type SessionList struct {
	Sessions      []SessionInfo `json:"sessions"`
	SessionID     string        `json:"session_id"`
	WindowFocused bool          `json:"window_focused"`
}

// CurrentSession 是最后选中的会话及窗口前台状态。
type CurrentSession struct {
	SessionInfo
	WindowFocused bool `json:"window_focused"`
}

// CreatedSession 是新建会话的稳定标识。
type CreatedSession struct {
	SessionID string `json:"session_id"`
	TabID     string `json:"tab_id"`
}

// Workspace 提供会话解析和工作区操作。ResolveSession 只解析一次，返回的对象不随焦点变化。
type Workspace interface {
	ResolveSession(string) (string, Executor, error)
	ListSessions() (SessionList, error)
	CurrentSession() (CurrentSession, error)
	NewTab(bool) (CreatedSession, error)
	RenameTab(string, string) error
	SplitSession(string, string, bool) (CreatedSession, error)
	FocusSession(string) error
}

const protocolVersion = 2

// clientConn 在同一连接先确认服务能力，再发送操作。
type clientConn struct {
	conn net.Conn
	read *bufio.Reader
}

func connectWorkspace(path string) (*clientConn, error) {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("无法连接 Who's Typing 窗口: %w", err)
	}
	c := &clientConn{conn: conn, read: bufio.NewReader(conn)}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(request{Action: "hello"}); err != nil {
		_ = conn.Close()
		return nil, errors.New("无法确认窗口版本，请升级 Who's Typing 窗口")
	}
	reply, err := c.reply()
	if err != nil || reply.Error != "" || reply.Version != protocolVersion || !reply.MultiSession {
		_ = conn.Close()
		return nil, errors.New("窗口不支持多会话，请升级 Who's Typing 窗口")
	}
	_ = conn.SetDeadline(time.Time{})
	return c, nil
}

func (c *clientConn) reply() (response, error) {
	line, err := c.read.ReadBytes('\n')
	if err != nil {
		return response{}, err
	}
	var reply response
	if err := json.Unmarshal(line, &reply); err != nil {
		return response{}, errors.New("终端响应格式错误")
	}
	return reply, nil
}

func (c *clientConn) send(req request) error {
	if err := json.NewEncoder(c.conn).Encode(req); err != nil {
		return fmt.Errorf("发送请求失败: %w", err)
	}
	return nil
}

func workspaceCall(path string, req request) (response, error) {
	c, err := connectWorkspace(path)
	if err != nil {
		return response{}, err
	}
	defer c.conn.Close()
	if err := c.send(req); err != nil {
		return response{}, err
	}
	reply, err := c.reply()
	if err != nil {
		return response{}, fmt.Errorf("终端连接已断开，操作结果未知: %w", err)
	}
	if reply.Error != "" {
		return response{}, errors.New(reply.Error)
	}
	return reply, nil
}

// ListWorkspace 查询会话列表。
func ListWorkspace() (SessionList, error) { return listWorkspace(socketPath(directory())) }
func listWorkspace(path string) (SessionList, error) {
	r, err := workspaceCall(path, request{Action: "session.list"})
	if err != nil {
		return SessionList{}, err
	}
	if r.List == nil {
		return SessionList{}, errors.New("会话列表响应格式错误")
	}
	return *r.List, nil
}

// CurrentWorkspace 查询最后选中的会话。
func CurrentWorkspace() (CurrentSession, error) { return currentWorkspace(socketPath(directory())) }
func currentWorkspace(path string) (CurrentSession, error) {
	r, err := workspaceCall(path, request{Action: "session.current"})
	if err != nil {
		return CurrentSession{}, err
	}
	if r.Current == nil {
		return CurrentSession{}, errors.New("当前会话响应格式错误")
	}
	return *r.Current, nil
}

// CreateTab 新建标签页。
func CreateTab(focus bool) (CreatedSession, error) {
	return createWorkspace(socketPath(directory()), request{Action: "tab.new", Focus: focus})
}

// RenameTab 按稳定标签页 ID 更新名称。
func RenameTab(id, title string) error {
	_, err := workspaceCall(socketPath(directory()), request{Action: "tab.rename", TabID: id, Title: title})
	return err
}

// SplitSession 在目标会话旁新建窗格。
func SplitSession(id, direction string, focus bool) (CreatedSession, error) {
	return createWorkspace(socketPath(directory()), request{Action: "session.split", SessionID: id, Direction: direction, Focus: focus})
}
func createWorkspace(path string, req request) (CreatedSession, error) {
	r, err := workspaceCall(path, req)
	if err != nil {
		return CreatedSession{}, err
	}
	if r.Created == nil || r.Created.SessionID == "" || r.Created.TabID == "" {
		return CreatedSession{}, errors.New("创建会话响应格式错误")
	}
	return *r.Created, nil
}

// FocusSession 聚焦指定会话。
func FocusSession(id string) error {
	_, err := workspaceCall(socketPath(directory()), request{Action: "session.focus", SessionID: id})
	return err
}

// RunSession 在绑定的会话执行命令。
func RunSession(id, command string, timeout time.Duration, out io.Writer) (int, error) {
	return runSession(socketPath(directory()), id, command, timeout, out)
}
func runSession(path, id, command string, timeout time.Duration, out io.Writer) (int, error) {
	c, err := connectWorkspace(path)
	if err != nil {
		return 1, err
	}
	defer c.conn.Close()
	if timeout > 0 {
		_ = c.conn.SetDeadline(time.Now().Add(timeout + 500*time.Millisecond))
	}
	if err := c.send(request{SessionID: id, Command: command, Timeout: int64(timeout)}); err != nil {
		if timeoutError(err) {
			return 1, errors.New("执行超时，结果未知，命令可能仍在执行")
		}
		return 1, err
	}
	r, err := c.reply()
	if err != nil {
		if timeoutError(err) {
			return 1, errors.New("执行超时，结果未知，命令可能仍在执行")
		}
		return 1, fmt.Errorf("终端连接已断开，结果未知: %w", err)
	}
	if r.Size < 0 {
		return 1, errors.New("终端响应格式错误")
	}
	if _, err := io.CopyN(out, c.read, r.Size); err != nil {
		if timeoutError(err) {
			return 1, errors.New("执行超时，结果未知，命令可能仍在执行")
		}
		return 1, fmt.Errorf("读取命令输出失败: %w", err)
	}
	if r.Error != "" {
		return 1, errors.New(r.Error)
	}
	return r.ExitCode, nil
}

// ReadSessionFor 读取绑定会话的终端快照。
func ReadSessionFor(id string, out io.Writer) error {
	return readSessionFor(socketPath(directory()), id, out)
}
func readSessionFor(path, id string, out io.Writer) error {
	c, err := connectWorkspace(path)
	if err != nil {
		return err
	}
	defer c.conn.Close()
	if err := c.send(request{Action: "session.read", SessionID: id}); err != nil {
		return err
	}
	r, err := c.reply()
	if err != nil {
		return fmt.Errorf("终端连接已断开: %w", err)
	}
	if r.Error != "" {
		return errors.New(r.Error)
	}
	if r.Size < 0 || r.Size > sessionReadLimit*utf8.UTFMax {
		return errors.New("终端响应格式错误")
	}
	_, err = io.CopyN(out, c.read, r.Size)
	return err
}

// ControlSession 查询或交还绑定会话的控制权。
func ControlSession(id, action string, out io.Writer) error {
	r, err := workspaceCall(socketPath(directory()), request{Action: action, SessionID: id})
	if err != nil {
		return err
	}
	if action == "session.status" {
		switch r.Control {
		case "human", "agent", "human_takeover":
			_, err = fmt.Fprintln(out, r.Control)
			return err
		}
		return errors.New("终端控制权响应格式错误")
	}
	return nil
}

// AttachSession 将远端接入绑定到指定会话。
func AttachSession(id, shellName string, auto bool, out io.Writer) error {
	return attachSession(socketPath(directory()), id, shellName, auto, out)
}
func attachSession(path, id, shellName string, auto bool, out io.Writer) error {
	c, err := connectWorkspace(path)
	if err != nil {
		return err
	}
	defer c.conn.Close()
	action := "attach"
	if auto {
		action = "attach.auto"
	}
	if err := c.send(request{Action: action, SessionID: id, Shell: shellName}); err != nil {
		return err
	}
	for {
		r, err := c.reply()
		if err != nil {
			return fmt.Errorf("终端连接已断开，接入结果未知: %w", err)
		}
		if r.Error != "" {
			return errors.New(r.Error)
		}
		switch r.Phase {
		case "instructions":
			if auto {
				return errors.New("终端响应格式错误")
			}
			_, err = fmt.Fprintf(out, "请在目标会话的远端 Bash 空提示符粘贴并执行以下一行：\n%s\n等待接入，按 Ctrl+C 可取消。\n", r.Script)
		case "confirmation":
			if !auto {
				return errors.New("终端响应格式错误")
			}
			_, err = fmt.Fprintln(out, "请在 Who's Typing 窗口确认目标会话的远端 Bash 空提示符。\n等待确认和接入，按 Ctrl+C 可取消。")
		case "attached":
			_, err = fmt.Fprintln(out, "远端 Bash 已接入。")
			return err
		default:
			return errors.New("终端响应格式错误")
		}
		if err != nil {
			return err
		}
	}
}
