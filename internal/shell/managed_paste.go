// 本文件处理受管窗格的粘贴：远端 tty 状态不可本地核验，一律先确认再发送；
// 远端完整写入并回执后才报告成功，不追加 Enter。
package shell

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"whos-typing/internal/control"
)

type managedPaste struct {
	id      string
	ctx     context.Context
	payload []byte
	done    chan struct{}
	err     error
	version uint64
	sent    bool
}

// PasteText 经确认后把文本粘贴到远端 PTY，等待远端写入回执。
func (s *ManagedSession) PasteText(ctx context.Context, text string, inspect func() PasteTarget, confirm func(context.Context) error) error {
	if err := control.ValidatePasteText(text); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	s.mu.Lock()
	if err := s.pastePrecheckLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.paste != nil {
		s.mu.Unlock()
		return errors.New("目标窗格已有粘贴请求")
	}
	version := s.pasteVersion
	s.mu.Unlock()
	target := inspect()
	if target.PasswordPrompt {
		return errors.New("目标窗格正在等待密码或隐藏输入")
	}
	s.mu.Lock()
	if err := s.pastePrecheckLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.paste != nil {
		s.mu.Unlock()
		return errors.New("目标窗格已有粘贴请求")
	}
	if s.pasteVersion != version {
		s.mu.Unlock()
		return errPasteChanged
	}
	s.pasteSeq++
	r := &managedPaste{id: strconv.FormatUint(s.pasteSeq, 10), ctx: ctx, done: make(chan struct{}), version: version}
	s.paste = r
	s.mu.Unlock()
	// 远端终端的规范模式/隐藏输入无法本地读取，粘贴始终需要目标窗格确认。
	if err := confirm(ctx); err != nil {
		s.mu.Lock()
		if s.paste == r {
			s.finishPasteLocked(r, err)
		}
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	if s.paste != r {
		err := r.err
		s.mu.Unlock()
		return err
	}
	if err := s.pastePrecheckLocked(); err != nil {
		s.finishPasteLocked(r, err)
		s.mu.Unlock()
		return err
	}
	if s.pasteVersion != r.version {
		s.finishPasteLocked(r, errPasteChanged)
		s.mu.Unlock()
		return errPasteChanged
	}
	r.payload = encodePaste(text, target.Mode)
	data, err := json.Marshal(map[string]string{
		"t": "paste", "id": r.id, "data": base64.StdEncoding.EncodeToString(r.payload),
	})
	if err != nil {
		s.finishPasteLocked(r, err)
		s.mu.Unlock()
		return err
	}
	s.enqueueLocked(managedOut{
		data:  append(data, '\n'),
		paste: r,
	})
	s.mu.Unlock()
	select {
	case <-r.done:
		return r.err
	case <-ctx.Done():
		s.mu.Lock()
		select {
		case <-r.done:
			err := r.err
			s.mu.Unlock()
			return err
		default:
		}
		if !r.sent {
			s.dropQueuedPasteLocked(r)
			s.finishPasteLocked(r, errors.New("粘贴已取消，未发送"))
			s.mu.Unlock()
			return errors.New("粘贴已取消，未发送")
		}
		// 已开始写入但无法确认完整到达。
		s.finishPasteLocked(r, control.ErrPasteUnknown)
		s.mu.Unlock()
		return control.ErrPasteUnknown
	}
}

// preparePasteWriteLocked 发送前再次核对确认。需要切换模式时，与粘贴合并写入。
func (s *ManagedSession) preparePasteWriteLocked(m *managedOut) bool {
	r := m.paste
	if s.paste != r {
		return false
	}
	if r.ctx.Err() != nil {
		s.finishPasteLocked(r, errors.New("粘贴已取消，未发送"))
		return false
	}
	if r.version != s.pasteVersion {
		s.finishPasteLocked(r, errPasteChanged)
		return false
	}
	if err := s.pastePrecheckLocked(); err != nil {
		s.finishPasteLocked(r, err)
		return false
	}
	if s.owner == ControlAgent {
		s.stopIdle()
		s.bashReady = false
		s.owner = ControlHuman
		if s.onControl != nil {
			s.onControl(ControlHuman)
		}
		m.data = append([]byte("{\"t\":\"human\"}\n"), m.data...)
	}
	r.sent = true
	return true
}

func (s *ManagedSession) pastePrecheckLocked() error {
	switch {
	case s.closed || s.disconnected:
		return errors.New("目标窗格不可用")
	case s.owner == ControlHumanTakeover:
		return errors.New("终端已由人类接管，请先交还 Agent")
	case s.active != nil || len(s.unconfirmed) != 0:
		return errors.New("目标窗格有其他 Agent 输入或结果未知，请先处理")
	case s.dirty || s.pending != 0:
		return errors.New("目标窗格有未提交输入，请先处理")
	case !s.humanPrompt && !(s.owner == ControlAgent && s.bashReady):
		return errors.New("目标窗格尚未停在空提示符，请等候后重新发起")
	}
	return nil
}

// finishPasteLocked 幂等：首个结果生效，迟到的回执不再改变状态。
func (s *ManagedSession) finishPasteLocked(r *managedPaste, err error) {
	if s.paste == r {
		s.paste = nil
	}
	select {
	case <-r.done:
		return
	default:
	}
	if errors.Is(err, control.ErrPasteUnknown) && !s.closed && !s.disconnected {
		// 远端可能留有部分文本，需要人类核对或清除后才允许执行命令。
		s.dirty = true
	}
	r.err = err
	close(r.done)
}
