// 本文件显示目标窗格确认并读取终端模型中的粘贴模式。
package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"
	"whos-typing/internal/shell"
)

type panePaste struct {
	text   string
	answer chan bool
	ctx    context.Context
	done   bool
}

func (c controller) Paste(ctx context.Context, text string, notify func() error) error {
	if c.pane == nil || c.pane.ctx == nil || c.pane.terminal == nil || c.update == nil {
		return errors.New("目标窗格不可用")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.pane.ctx, cancel)
	defer stop()
	term := c.pane.terminal
	return c.Session.PasteText(ctx, text, func() shell.PasteTarget {
		target := pasteMode(term.Snapshot())
		target.PasswordPrompt = isPasswordPrompt(term.Text())
		return target
	}, func(wait context.Context) error {
		return c.confirmPaste(wait, text, notify)
	})
}

func (c controller) confirmPaste(ctx context.Context, text string, notify func() error) error {
	if err := notify(); err != nil {
		return err
	}
	req := &panePaste{text: text, answer: make(chan bool, 1), ctx: ctx}
	c.update(func() {
		if ctx.Err() == nil {
			c.pane.paste = req
		}
	})
	defer c.update(func() {
		if c.pane.paste == req {
			c.pane.paste = nil
		}
	})
	select {
	case accepted := <-req.answer:
		if !accepted {
			return errors.New("用户取消粘贴")
		}
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *panePaste) view(c *ui.Context, id, tabID string) {
	if r.done || r.ctx.Err() != nil {
		return
	}
	background := ui.Hex("#f2f4f7")
	if c.Theme().Dark {
		background = ui.Hex("#242931")
	}
	ui.Column(c.Key("paste-confirmation-"+id)).Attach(ui.AnchorTop, ui.AnchorTop).Top(8).
		WidthPercent(95).MaxWidth(540).Padding(10).Gap(6).Radius(10).Background(background).
		Children(func() {
			ui.Text(c, fmt.Sprintf("确认粘贴到 %s（%s）", id, tabID)).Bold()
			message := "请核对当前前台程序和待粘贴文本。"
			if strings.ContainsAny(r.text, "\r\n") {
				message += " 文本中的换行可能立即提交。"
			}
			ui.Text(c, message)
			ui.Scroll(c.Key("paste-preview-" + id)).FillWidth().MaxHeight(120).Children(func() {
				// 将每个换行和制表符标出来，保留全文和末尾换行供审阅。
				shown := strings.NewReplacer("\r", "␍", "\n", "␊\n", "\t", "⇥\t").Replace(r.text)
				ui.Text(c, shown).Font("Menlo").FillWidth()
			})
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if ui.Button(c.Key("paste-approve-"+id), "确认粘贴").KeepFocus().Clicked() {
					r.respond(true)
				}
				if ui.Button(c.Key("paste-cancel-"+id), "取消").KeepFocus().Clicked() {
					r.respond(false)
				}
			})
		})
}

func (r *panePaste) respond(approved bool) {
	if !r.done && r.ctx.Err() == nil {
		r.done = true
		r.answer <- approved
	}
}

var passwordPrompt = regexp.MustCompile("(?i)(?:\\b(?:password|passphrase)(?: for [^:\\n]+)?|密码|口令)\\s*[:：]$")

func isPasswordPrompt(text string) bool {
	line := strings.TrimSpace(text)
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	return passwordPrompt.MatchString(line)
}

// pasteMode 从终端格式化的 VT 状态读取 DEC 模式，并跳过 OSC 等控制字符串。
func pasteMode(snapshot []byte) shell.PasteTarget {
	if len(snapshot) == 0 {
		return shell.PasteTarget{}
	}
	mode := shell.PasteTarget{Mode: shell.PastePlain}
	for len(snapshot) != 0 {
		// 范围外的文字，包括多字节 UTF-8，不能开启 VT 控制序列。
		at := bytes.IndexByte(snapshot, 0x1b)
		if at < 0 {
			break
		}
		snapshot = snapshot[at+1:]
		if len(snapshot) == 0 {
			return shell.PasteTarget{}
		}
		prefix := snapshot[0]
		snapshot = snapshot[1:]
		switch prefix {
		case ']', 'P', '_', '^', 'X':
			ended := false
			for i, b := range snapshot {
				if prefix == ']' && b == 7 {
					snapshot = snapshot[i+1:]
					ended = true
					break
				}
				if b == 0x1b && i+1 < len(snapshot) && snapshot[i+1] == '\\' {
					snapshot = snapshot[i+2:]
					ended = true
					break
				}
			}
			if !ended {
				return shell.PasteTarget{}
			}
		case '[':
			end := 0
			for end < len(snapshot) && snapshot[end] >= 0x20 && snapshot[end] < 0x40 {
				end++
			}
			if end == len(snapshot) || snapshot[end] < 0x40 || snapshot[end] > 0x7e {
				return shell.PasteTarget{}
			}
			if end > 1 && snapshot[0] == '?' && (snapshot[end] == 'h' || snapshot[end] == 'l') {
				enabled := snapshot[end] == 'h'
				for _, n := range strings.Split(string(snapshot[1:end]), ";") {
					switch n {
					case "2004":
						mode.Mode = shell.PastePlain
						if enabled {
							mode.Mode = shell.PasteBracketed
						}
					case "5522":
						mode.ClipboardOffer = enabled
					}
				}
			}
			snapshot = snapshot[end+1:]
		case 'c':
			mode = shell.PasteTarget{Mode: shell.PastePlain}
		}
	}
	return mode
}
