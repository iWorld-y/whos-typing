// 本文件处理指定窗格的粘贴及其发送回执。
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode"
	"unicode/utf8"
)

const maxPasteBytes = 64 << 10

// ErrPasteUnknown 表示终端可能收到部分或全部文本，调用方需要核对窗格。
var ErrPasteUnknown = errors.New("粘贴发送状态未知")

type pasteWorkspace interface{ SupportsSessionPaste() bool }

// SessionPaster 向已绑定的窗格发送文本，必要时通过回调提示 CLI 等待窗口确认。
type SessionPaster interface {
	Paste(context.Context, string, func() error) error
}

// ValidatePasteText 检查粘贴内容是有效 UTF-8 文本，只允许换行和制表相关的控制字符。
func ValidatePasteText(text string) error {
	if text == "" || len(text) > maxPasteBytes || !utf8.ValidString(text) {
		return errors.New("粘贴内容须为非空 UTF-8 文本，最多 64 KiB")
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\t' && r != '\r' && r != '\n' {
			return errors.New("粘贴内容含有非文本控制字符")
		}
	}
	return nil
}

func handlePaste(ctx context.Context, out io.Writer, target Executor, text string) {
	write := json.NewEncoder(out).Encode
	paster, ok := target.(SessionPaster)
	if !ok {
		_ = write(response{Error: "未发送粘贴：目标窗格不支持粘贴"})
		return
	}
	err := ValidatePasteText(text)
	if err == nil {
		err = paster.Paste(ctx, text, func() error { return write(response{Phase: "confirmation"}) })
	}
	reply := response{Phase: "sent"}
	switch {
	case errors.Is(err, ErrPasteUnknown):
		reply.Phase, reply.Error = "unknown", "请核对目标窗格后再操作"
	case err != nil:
		reply.Phase, reply.Error = "not_sent", "未发送粘贴："+err.Error()
	}
	_ = write(reply)
}

// PasteSession 从输入流读取文本，再向指定的窗格发起一次粘贴。
func PasteSession(id string, in io.Reader, out, status io.Writer) error {
	return pasteSession(socketPath(directory()), id, in, out, status)
}

func pasteSession(path, id string, in io.Reader, out, status io.Writer) error {
	if id == "" {
		return errors.New("未发送粘贴：请指定完整会话 ID")
	}
	body, err := io.ReadAll(io.LimitReader(in, maxPasteBytes+1))
	if err != nil {
		return fmt.Errorf("未发送粘贴：读取标准输入失败: %w", err)
	}
	if err := ValidatePasteText(string(body)); err != nil {
		return fmt.Errorf("未发送粘贴：%w", err)
	}
	c, err := connectWorkspace(path)
	if err != nil {
		return fmt.Errorf("未发送粘贴：%w", err)
	}
	defer c.conn.Close()
	if !c.sessionPaste {
		return errors.New("未发送粘贴：当前窗口不支持终端粘贴，请升级 Who's Typing 窗口")
	}
	if err := c.send(request{Action: "session.paste", SessionID: id, Text: string(body)}); err != nil {
		return fmt.Errorf("%w，请核对目标窗格后再操作: %v", ErrPasteUnknown, err)
	}
	for {
		reply, err := c.reply()
		if err != nil {
			return fmt.Errorf("%w，请核对目标窗格后再操作: %v", ErrPasteUnknown, err)
		}
		if reply.Error != "" {
			if reply.Phase == "unknown" {
				return fmt.Errorf("%w：%s", ErrPasteUnknown, reply.Error)
			}
			return errors.New(reply.Error)
		}
		switch reply.Phase {
		case "confirmation":
			if _, err := fmt.Fprintln(status, "请在目标窗格核对待粘贴文本。等待确认，按 Ctrl+C 可取消。"); err != nil {
				return fmt.Errorf("%w：无法显示确认提示: %v", ErrPasteUnknown, err)
			}
		case "sent":
			_, err = fmt.Fprintln(out, "已发送粘贴。")
			return err
		default:
			return fmt.Errorf("%w：终端响应格式错误", ErrPasteUnknown)
		}
	}
}
