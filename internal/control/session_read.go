// 本文件提供终端屏幕与滚动历史的只读快照。
package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
	"unicode/utf8"
)

const sessionReadLimit = 10000

func handleSessionRead(conn net.Conn, executor Executor) {
	reader, ok := executor.(SessionReader)
	if !ok {
		_ = json.NewEncoder(conn).Encode(response{Error: "终端不支持读取会话"})
		return
	}
	text, err := reader.ReadSession()
	if err != nil {
		_ = json.NewEncoder(conn).Encode(response{Error: err.Error()})
		return
	}
	text = strings.Trim(strings.ToValidUTF8(text, "\uFFFD"), "\r\n")
	text = compressRepeatedTail(text)
	start := len(text)
	for count := 0; start > 0 && count < sessionReadLimit; count++ {
		_, size := utf8.DecodeLastRuneInString(text[:start])
		start -= size
	}
	text = strings.Trim(text[start:], "\r\n")
	if err := json.NewEncoder(conn).Encode(response{Size: int64(len(text))}); err == nil {
		_, _ = io.WriteString(conn, text)
	}
}

// compressRepeatedTail 仅在缩短文本时合并末尾连续的相同行。
func compressRepeatedTail(text string) string {
	first := strings.LastIndexByte(text, '\n') + 1
	if first == 0 {
		return text
	}
	line := text[first:]
	count := 1
	for first > 0 {
		end := first - 1
		if end > 0 && text[end-1] == '\r' {
			end--
		}
		start := strings.LastIndexByte(text[:end], '\n') + 1
		if text[start:end] != line {
			break
		}
		first = start
		count++
	}
	if count < 2 {
		return text
	}
	compressed := fmt.Sprintf("%s (重复 %d 行)", line, count)
	if utf8.RuneCountInString(compressed) >= utf8.RuneCountInString(text[first:]) {
		return text
	}
	return text[:first] + compressed
}

// ReadSession 输出窗口当前文本的末尾最多 10000 个 Unicode 字符。
func ReadSession(out io.Writer) error {
	return readSession(socketPath(directory()), out)
}

func readSession(path string, out io.Writer) error {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return fmt.Errorf("无法连接 Who's Typing 窗口: %w", err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(request{Action: "session.read"}); err != nil {
		return fmt.Errorf("发送会话读取请求失败: %w", err)
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("终端连接已断开: %w", err)
	}
	var reply response
	if err := json.Unmarshal(line, &reply); err != nil || reply.Size < 0 || reply.Size > sessionReadLimit*utf8.UTFMax {
		return errors.New("终端响应格式错误")
	}
	if reply.Error != "" {
		return errors.New(reply.Error)
	}
	if _, err := io.CopyN(out, r, reply.Size); err != nil {
		return fmt.Errorf("读取会话文本失败: %w", err)
	}
	return nil
}
