// 本文件验证、排队和写入已授权的终端粘贴。
package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"whos-typing/internal/control"
)

var errPasteChanged = errors.New("目标窗格状态已变化，请重新核对后发起粘贴")

// PasteMode 表示终端模型观测到的直接文本粘贴模式。
type PasteMode uint8

const (
	PasteUnknown PasteMode = iota
	PastePlain
	PasteBracketed
)

// PasteTarget 包含终端模型中与粘贴有关的状态。
type PasteTarget struct {
	Mode           PasteMode
	PasswordPrompt bool
	ClipboardOffer bool
}

type pasteState struct {
	target   PasteTarget
	group    int
	ready    bool
	ttyKnown bool
	ttyMode  uint32
}

type pasteRequest struct {
	ctx       context.Context
	text      string
	inspect   func() PasteTarget
	state     pasteState
	barrier   []byte
	done      chan struct{}
	refresh   chan struct{}
	err       error
	writing   bool
	confirmed bool
	changed   bool
}

func (r *pasteRequest) needsConfirmation() bool {
	return !r.state.ready || strings.ContainsAny(r.text, "\r\n") && (r.state.target.Mode != PasteBracketed || r.state.target.ClipboardOffer)
}

// PasteText 在输入队列中串行发送文本，必要时先等待目标窗格确认。
func (s *Session) PasteText(ctx context.Context, text string, inspect func() PasteTarget, confirm func(context.Context) error) error {
	if err := control.ValidatePasteText(text); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	r := &pasteRequest{ctx: ctx, text: text, inspect: inspect, done: make(chan struct{}), refresh: make(chan struct{}, 1)}
	s.mu.Lock()
	if s.paste != nil {
		s.mu.Unlock()
		return errors.New("目标窗格已有粘贴请求")
	}
	s.paste = r
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.paste == r && !r.writing {
			s.finishPaste(r, ctx.Err())
		}
		s.mu.Unlock()
	}()
	for forceConfirmation := false; ; forceConfirmation = true {
		if err := s.preparePaste(r); err != nil {
			return err
		}
		needsConfirmation := forceConfirmation || r.needsConfirmation()
		if needsConfirmation {
			wait, stop := context.WithCancel(ctx)
			answer := make(chan error, 1)
			go func() { answer <- confirm(wait) }()
			var err error
			select {
			case err = <-answer:
			case <-r.done:
				err = r.err
			case <-ctx.Done():
				err = ctx.Err()
			}
			stop()
			if err != nil {
				return err
			}
		}
		s.mu.Lock()
		if s.paste != r {
			s.mu.Unlock()
			return r.err
		}
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return err
		}
		r.confirmed = needsConfirmation
		s.pasteSequence++
		r.barrier = fmt.Appendf(nil, "\x00WT-PASTE:%s:%d", s.token, s.pasteSequence)
		s.send(r.barrier)
		s.mu.Unlock()
		select {
		case <-r.done:
			return r.err
		case <-r.refresh:
			// 免确认资格在写入前消失。以新状态发起一次目标窗格确认。
		case <-ctx.Done():
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.paste != r {
				return r.err
			}
			if !r.writing {
				s.finishPaste(r, ctx.Err())
				return ctx.Err()
			}
			_ = s.pty.SetWriteDeadline(time.Now())
			return control.ErrPasteUnknown
		}
	}
}

// preparePaste 确保 VT 格式器已经看到了会话读出的最新输出。
func (s *Session) preparePaste(r *pasteRequest) error {
	for {
		s.mu.Lock()
		if s.paste != r {
			s.mu.Unlock()
			return r.err
		}
		if s.pasteOutputPending == nil {
			state, err := s.pasteState(r.inspect)
			r.state = state
			r.changed = false
			s.mu.Unlock()
			return err
		}
		parsed := s.pasteOutputPending
		s.mu.Unlock()
		select {
		case <-parsed:
		case <-r.done:
			return r.err
		case <-r.ctx.Done():
			return r.ctx.Err()
		}
	}
}

// pasteState 检查不能通过确认绕过的保护，并记录用于发送前复核的状态。
func (s *Session) pasteState(inspect func() PasteTarget) (pasteState, error) {
	if s.closed || s.send == nil || s.pty == nil {
		return pasteState{}, errors.New("目标窗格不可用")
	}
	if s.owner == ControlHumanTakeover {
		return pasteState{}, errors.New("终端已由人类接管，请先交还 Agent")
	}
	if s.queued != nil || len(s.unconfirmed) != 0 || (s.remote != nil && (s.remote.attaching || s.remote.orphan != nil)) {
		return pasteState{}, errors.New("目标窗格有其他 Agent 输入或结果未知，请先处理")
	}
	if r := s.active; r != nil && (!r.started || r.injecting || len(r.inject) != 0 || r.finished) {
		return pasteState{}, errors.New("目标窗格的 Agent 命令正在发送或切换状态，请等候后重新发起")
	}
	group, _ := s.foregroundGroup()
	if s.dirty || (s.remote != nil && s.remote.dirty) || (s.foregroundEdit && s.foregroundEditGroup == group) {
		return pasteState{}, errors.New("目标窗格有未提交输入，请先处理")
	}
	target := inspect()
	termios, sawTTY := s.pasteTermios()
	if target.PasswordPrompt || (sawTTY && termios.Lflag&syscall.ICANON != 0 && termios.Lflag&syscall.ECHO == 0) {
		return pasteState{}, errors.New("目标窗格正在等待密码或隐藏输入")
	}
	ready := s.pasteOutputPending == nil && sawTTY && group > 1 && s.command != nil && s.command.Process != nil && group == s.command.Process.Pid && s.remote == nil && s.active == nil && s.ready && s.pending == 0
	if s.pasteOutputPending != nil {
		target.Mode = PasteUnknown
	}
	return pasteState{target: target, group: group, ready: ready, ttyKnown: sawTTY, ttyMode: uint32(termios.Lflag & (syscall.ICANON | syscall.ECHO))}, nil
}

// invalidatePaste 作废尚未写入的请求，防止输入、输出或接管后复用确认。
func (s *Session) invalidatePaste(err error) {
	if r := s.paste; r != nil && !r.writing {
		s.finishPaste(r, err)
	}
}

// notePasteOutput 等待终端解析，并让已展示的确认失效。
func (s *Session) notePasteOutput() {
	if s.pasteOutputPending == nil {
		s.pasteOutputPending = make(chan struct{})
	}
	if r := s.paste; r != nil && !r.writing && r.barrier != nil && !r.confirmed {
		r.changed = true
		return
	}
	s.invalidatePaste(errPasteChanged)
}

func (s *Session) finishPaste(r *pasteRequest, err error) {
	s.paste = nil
	r.err = err
	close(r.done)
}

func (s *Session) writePaste(p []byte) {
	r := s.paste
	if r == nil || !bytes.Equal(r.barrier, p) {
		return
	}
	if err := r.ctx.Err(); err != nil {
		s.finishPaste(r, err)
		return
	}
	current, err := s.pasteState(r.inspect)
	if err != nil {
		s.finishPaste(r, err)
		return
	}
	if current != r.state || r.changed || s.pasteOutputPending != nil {
		if !r.confirmed {
			r.barrier = nil
			r.refresh <- struct{}{}
		} else {
			s.finishPaste(r, errPasteChanged)
		}
		return
	}
	payload := encodePaste(r.text, current.target.Mode)
	r.writing = true
	previousOwner := s.owner
	s.stopIdle()
	s.setControl(ControlAgent)
	s.trackPasteInput(current, payload)
	n, err := s.writePTY(payload)
	_ = s.pty.SetWriteDeadline(time.Time{})
	if s.owner == ControlAgent {
		s.setControl(previousOwner)
		if previousOwner == ControlAgent {
			s.startIdle()
		}
	}
	if n != len(payload) || err != nil {
		s.finishPaste(r, control.ErrPasteUnknown)
		return
	}
	s.finishPaste(r, nil)
}

// trackPasteInput 更新 Shell 自己的输入追踪，不把前台程序的输入计为 Shell 提交。
func (s *Session) trackPasteInput(state pasteState, payload []byte) {
	if s.active != nil {
		return
	}
	if r := s.remote; r != nil {
		r.ready = false
		r.trackInput(s, payload)
		return
	}
	s.ready = false
	groupKnown := state.group > 1 && s.command != nil && s.command.Process != nil
	if !groupKnown || state.group == s.command.Process.Pid {
		if state.target.Mode == PasteBracketed {
			s.dirty = true
		} else {
			s.trackInput(payload)
		}
	}
}

// encodePaste 使用直接文本协议，保留括号粘贴的换行并将普通粘贴换行编码为回车。
func encodePaste(text string, mode PasteMode) []byte {
	if mode == PasteBracketed {
		return []byte("\x1b[200~" + text + "\x1b[201~")
	}
	return []byte(strings.NewReplacer("\r\n", "\r", "\n", "\r").Replace(text))
}

// trackForegroundEdit 记录前台程序中可观察的人工输入，不把 Shell 的 pending 当作输入行。
func (s *Session) trackForegroundEdit(p []byte) {
	group, err := s.foregroundGroup()
	if err != nil {
		return
	}
	if s.foregroundEditGroup != group {
		s.foregroundEditGroup = group
		s.foregroundEdit = false
	}
	if m := kittyKey.FindSubmatch(p); len(m) != 0 {
		s.foregroundEdit = string(m[1]) != "13" && !isInterrupt(p)
		return
	}
	if bytes.HasPrefix(p, []byte("\x1b[200~")) && bytes.HasSuffix(p, []byte("\x1b[201~")) {
		s.foregroundEdit = true
		return
	}
	for _, b := range p {
		s.foregroundEdit = b != '\r' && b != '\n' && b != 3
	}
}

func (s *Session) pasteTermios() (syscall.Termios, bool) {
	var tty syscall.Termios
	raw, err := s.pty.SyscallConn()
	if err != nil {
		return tty, false
	}
	var ioctlErr syscall.Errno
	err = raw.Control(func(fd uintptr) {
		_, _, ioctlErr = syscall.Syscall(syscall.SYS_IOCTL, fd, pasteTermiosRequest, uintptr(unsafe.Pointer(&tty)))
	})
	return tty, err == nil && ioctlErr == 0
}
