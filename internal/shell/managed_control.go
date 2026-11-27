// 本文件记录受管会话的控制权、空闲计时和只读诊断。
package shell

import (
	"errors"
	"time"

	"whos-typing/internal/control"
)

// ControlOwner 返回当前控制方。
func (s *ManagedSession) ControlOwner() ControlOwner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owner
}

// ControlStatus 返回适用于 CLI 的控制方名称。
func (s *ManagedSession) ControlStatus() string {
	switch s.ControlOwner() {
	case ControlAgent:
		return "agent"
	case ControlHumanTakeover:
		return "human_takeover"
	default:
		return "human"
	}
}

// SetControlHandler 设置控制权变化回调；回调不得等待会话或窗口线程。
func (s *ManagedSession) SetControlHandler(fn func(ControlOwner)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onControl = fn
}

// SetStateHandler 设置 CLI 执行状态回调。
func (s *ManagedSession) SetStateHandler(fn func(bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onState = fn
}

func (s *ManagedSession) state(active bool) {
	if s.onState != nil {
		s.onState(active)
	}
}

func (s *ManagedSession) setControl(owner ControlOwner) {
	if s.owner == owner {
		return
	}
	if r := s.paste; r != nil {
		if !r.sent {
			s.dropQueuedPasteLocked(r)
			s.finishPasteLocked(r, errPasteChanged)
		} else {
			s.finishPasteLocked(r, control.ErrPasteUnknown)
		}
	}
	s.owner = owner
	if s.onControl != nil {
		s.onControl(owner)
	}
}

// TakeOver 让人类立即取得输入控制权，不中断已经运行的命令。
func (s *ManagedSession) TakeOver() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != ControlAgent || s.closed || s.disconnected {
		return
	}
	s.stopIdle()
	if r := s.active; r != nil && !r.sent {
		s.dropQueuedLocked(r)
		s.active = nil
		s.state(false)
		s.finish(r, 1, errors.New("终端已由人类接管，命令未执行"))
	}
	s.setControl(ControlHumanTakeover)
	if s.active == nil {
		s.bashReady = false
		s.sendMessageLocked(map[string]any{"t": "human"}, nil)
	}
}

// ReturnControl 在 Bash 就绪且没有未提交输入时把控制权交还 Agent。
func (s *ManagedSession) ReturnControl() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.disconnected {
		return errors.New("受管会话已结束，请显式新建受管会话")
	}
	if s.owner != ControlHumanTakeover {
		return errors.New("终端当前没有由人类接管")
	}
	if !s.humanPrompt || s.dirty || s.pending != 0 || s.active != nil || s.paste != nil || len(s.unconfirmed) != 0 {
		return errors.New("终端尚未就绪，请完成当前输入或等待前台程序")
	}
	s.humanPrompt = false
	s.bashReady = false
	s.sendMessageLocked(map[string]any{"t": "release"}, nil)
	s.setControl(ControlAgent)
	return nil
}

// ReleaseControl 让 Agent 在空闲时主动交还控制权。
func (s *ManagedSession) ReleaseControl() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.disconnected {
		return errors.New("受管会话已结束，请显式新建受管会话")
	}
	if s.owner == ControlHumanTakeover {
		return errors.New("终端已由人类接管，请等待交还 Agent")
	}
	if len(s.unconfirmed) != 0 {
		return errors.New("此前命令结果未知，请在目标窗格接管并等待完成通知")
	}
	if s.active != nil || s.paste != nil {
		return errors.New("命令仍在执行，请等待终端空闲后再交还")
	}
	s.stopIdle()
	if s.owner == ControlAgent && s.bashReady {
		s.bashReady = false
		s.sendMessageLocked(map[string]any{"t": "human"}, nil)
	}
	s.setControl(ControlHuman)
	return nil
}

// SessionDiagnostic 返回只读控制和受管会话状态。
func (s *ManagedSession) SessionDiagnostic() control.Diagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := "human"
	if s.owner == ControlAgent {
		owner = "agent"
	} else if s.owner == ControlHumanTakeover {
		owner = "human_takeover"
	}
	state := "starting"
	switch {
	case s.disconnected:
		state = "disconnected"
	case s.commandRunning, s.humanRunning:
		state = "running"
	case s.bashReady, s.humanPrompt:
		state = "ready"
	}
	unknown := s.unknownOnDisconnect || len(s.unconfirmed) != 0
	action := "none"
	switch {
	case s.disconnected:
		action = "重新创建受管 SSH 会话（ssh open）"
	case unknown:
		action = "在目标窗格接管，等待命令完成通知"
	case state == "running":
		action = "等待当前命令完成，或在目标窗格接管"
	}
	return control.Diagnostic{Control: owner, Remote: state, Unknown: unknown, SuggestedAction: action, Available: true}
}

// TitleContext 返回最近确认的远端上下文。
func (s *ManagedSession) TitleContext() TitleContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.titleContext
}

// SetTitleHandler 在上下文变化时通知调用方。
func (s *ManagedSession) SetTitleHandler(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onTitle = fn
}

func (s *ManagedSession) setTitleContext(value TitleContext) {
	if s.titleContext == value {
		return
	}
	s.titleContext = value
	if s.onTitle != nil {
		s.onTitle()
	}
}

// stopIdle、startIdle 和 armIdle 均由持有 s.mu 的调用方调用。
func (s *ManagedSession) stopIdle() {
	s.idleVersion++
	if s.idleCancel != nil {
		s.idleCancel()
		s.idleCancel = nil
	}
	s.idleStarted = time.Time{}
}

func (s *ManagedSession) startIdle() {
	s.stopIdle()
	if s.owner != ControlAgent {
		return
	}
	s.idleStarted = s.nowIdle()
	s.armIdle()
}

func (s *ManagedSession) nowIdle() time.Time {
	if s.idleNow != nil {
		return s.idleNow()
	}
	return time.Now()
}

func (s *ManagedSession) armIdle() {
	if s.owner != ControlAgent || s.idleStarted.IsZero() || s.idleCancel != nil || s.closed {
		return
	}
	delay := max(0, controlIdleTimeout-s.nowIdle().Sub(s.idleStarted))
	version := s.idleVersion
	expire := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.owner != ControlAgent || version != s.idleVersion || s.closed || s.disconnected {
			return
		}
		s.idleCancel = nil
		if !s.bashReady || s.active != nil || s.paste != nil || s.dirty || s.pending != 0 || len(s.unconfirmed) != 0 {
			return
		}
		s.stopIdle()
		s.bashReady = false
		s.sendMessageLocked(map[string]any{"t": "human"}, nil)
		s.setControl(ControlHuman)
	}
	if s.idleAfter != nil {
		s.idleCancel = s.idleAfter(delay, expire)
	} else {
		timer := time.AfterFunc(delay, expire)
		s.idleCancel = func() { timer.Stop() }
	}
}
