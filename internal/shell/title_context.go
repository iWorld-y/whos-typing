// 本文件解析提示符上报的用户、主机和工作目录。
package shell

import (
	"encoding/base64"
	"os"
	"os/user"
	"strings"
	"unicode/utf8"
)

const maxTitleMarker = 8192

// TitleContext 是最近确认的 Shell 上下文。
type TitleContext struct {
	User, Host, Directory string
	Unknown               bool
}

// InitialTitleContext 返回启动本地 Shell 时使用的初始上下文。
func InitialTitleContext() TitleContext {
	name := os.Getenv("USER")
	if current, err := user.Current(); err == nil && current.Username != "" {
		name = current.Username
	}
	host, _ := os.Hostname()
	dir, _ := os.UserHomeDir()
	return TitleContext{User: name, Host: host, Directory: dir}
}

// TitleContext 返回最近确认的标题上下文，不改变终端状态。
func (s *Session) TitleContext() TitleContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.titleContext
}

// SetTitleHandler 在上下文变化时通知调用方，回调不得等待 Shell 或窗口线程。
func (s *Session) SetTitleHandler(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTitle = fn
}

func (s *Session) setTitleContext(value TitleContext) {
	if s.titleContext == value {
		return
	}
	s.titleContext = value
	if s.onTitle != nil {
		s.onTitle()
	}
}

func (s *Session) markTitleUnknown() {
	value := s.titleContext
	value.Unknown = true
	s.setTitleContext(value)
}

func decodeTitleContext(mark string) (TitleContext, bool) {
	parts := strings.Split(mark, ":")
	if len(parts) != 4 || parts[0] != "M" || len(mark) > maxTitleMarker {
		return TitleContext{}, false
	}
	values := make([]string, 3)
	for i, part := range parts[1:] {
		data, err := base64.StdEncoding.Strict().DecodeString(part)
		limit := 256
		if i == 2 {
			limit = 4096
		}
		if err != nil || len(data) == 0 || len(data) > limit || !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
			return TitleContext{}, false
		}
		values[i] = string(data)
	}
	return TitleContext{User: values[0], Host: values[1], Directory: values[2]}, true
}

// titlePrompt 使用 Base64 封装字段，Shell 保存并恢复原有退出码。
func titlePrompt(mark string, fish bool) string {
	if fish {
		return `set -l wt_user (command id -un 2>/dev/null); set -l wt_host (command hostname 2>/dev/null); ` +
			`set -l wt_u (printf '%s' "$wt_user" | command base64 2>/dev/null | command tr -d '\n' 2>/dev/null); ` +
			`set -l wt_h (printf '%s' "$wt_host" | command base64 2>/dev/null | command tr -d '\n' 2>/dev/null); ` +
			`set -l wt_d (printf '%s' "$PWD" | command base64 2>/dev/null | command tr -d '\n' 2>/dev/null); ` +
			`printf '` + mark + `M:%s:%s:%s\037' "$wt_u" "$wt_h" "$wt_d"; `
	}
	return `local wt_user wt_host wt_u wt_h wt_d; wt_user=$(command id -un 2>/dev/null); wt_host=$(command hostname 2>/dev/null); ` +
		`wt_u=$(printf '%s' "$wt_user" | command base64 2>/dev/null | command tr -d '\n' 2>/dev/null); ` +
		`wt_h=$(printf '%s' "$wt_host" | command base64 2>/dev/null | command tr -d '\n' 2>/dev/null); ` +
		`wt_d=$(printf '%s' "$PWD" | command base64 2>/dev/null | command tr -d '\n' 2>/dev/null); ` +
		`printf '` + mark + `M:%s:%s:%s\037' "$wt_u" "$wt_h" "$wt_d"; `
}
