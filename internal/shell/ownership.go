// 本文件记录人类与 Agent 对终端输入的控制权。
package shell

import (
	"errors"
	"time"

	"whos-typing/internal/control"
)

// ControlOwner 表示当前终端输入由谁控制。
type ControlOwner uint8

const (
	// ControlHuman 表示人类可输入，CLI 可在终端空闲时获取控制权。
	ControlHuman ControlOwner = iota
	// ControlAgent 表示 CLI 控制终端，人类可通过接管取回控制权。
	ControlAgent
	// ControlHumanTakeover 表示人类主动接管，CLI 须等待人类交还。
	ControlHumanTakeover
)

const controlIdleTimeout = time.Minute

// ControlOwner 返回当前控制方。
func (s *Session) ControlOwner() ControlOwner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.owner
}

// ControlStatus 返回适用于 CLI 的控制方名称。
func (s *Session) ControlStatus() string {
	switch s.ControlOwner() {
	case ControlAgent:
		return "agent"
	case ControlHumanTakeover:
		return "human_takeover"
	default:
		return "human"
	}
}

// SetControlHandler 设置控制权变化回调；回调不得等待 Shell 或窗口线程。
func (s *Session) SetControlHandler(fn func(ControlOwner)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onControl = fn
}

// setControl 由持有 s.mu 的调用方调用。
func (s *Session) setControl(owner ControlOwner) {
	if s.owner == owner {
		return
	}
	s.owner = owner
	if s.onControl != nil {
		s.onControl(owner)
	}
}

// restoreQueuedControl 在排队命令未执行时恢复原有控制状态。
func (s *Session) restoreQueuedControl(r *request) {
	if r.humanBefore && s.owner == ControlAgent {
		s.setControl(ControlHuman)
		return
	}
	s.startIdle()
}

// TakeOver 让人类立即取得输入控制权，不中断已经运行的命令。
func (s *Session) TakeOver() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == ControlAgent {
		s.stopIdle()
		s.setControl(ControlHumanTakeover)
		if r := s.queued; r != nil {
			s.queued = nil
			s.finish(r, 1, errors.New("终端已由人类接管，排队命令未执行"))
		}
		if r := s.active; r != nil && r.remote && len(r.inject) != 0 && !r.injecting {
			s.active = nil
			r.remoteBinding.ready = false
			r.inject = nil
			s.state(false)
			s.finish(r, 1, errors.New("终端已由人类接管，远端命令未发送"))
		}
		if r := s.active; r != nil && r.injecting {
			r.resetWriteDeadline = true
			_ = s.pty.SetWriteDeadline(time.Now())
		}
		if r := s.remote; r != nil && r.attaching {
			if r.autoCancel != nil {
				r.autoCancel()
				r.autoCancel = nil
			}
			if len(r.auto) != 0 && !r.injected {
				r.auto = nil
				s.failAttach(r, errors.New("终端已由人类接管，远端接入未发送"))
			} else if r.injecting {
				r.resetWrite = true
				_ = s.pty.SetWriteDeadline(time.Now())
			}
		}
	}
}

// ReturnControl 在终端空闲时将人类接管的控制权交还 Agent。
func (s *Session) ReturnControl() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("终端会话已结束")
	}
	if s.owner != ControlHumanTakeover {
		return errors.New("终端当前没有由人类接管")
	}
	ready := s.ready && !s.dirty && s.pending == 0
	if r := s.remote; r != nil {
		ready = !r.attaching && r.ready && len(r.idleOutput) == 0 && !r.dirty && r.pending == 0 && r.orphan == nil
	}
	if !ready || s.queued != nil || s.active != nil || len(s.unconfirmed) != 0 {
		if s.remote != nil && s.canRecoverRemote() == nil {
			return ErrRemoteNotReady
		}
		return errors.New("终端尚未就绪，请完成当前输入或等待前台程序")
	}
	s.setControl(ControlAgent)
	s.startIdle()
	return nil
}

// ReleaseControl 让 Agent 在命令结束后主动交还控制权。
func (s *Session) ReleaseControl() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("终端会话已结束")
	}
	if s.owner == ControlHumanTakeover {
		return errors.New("终端已由人类接管，请等待交还 Agent")
	}
	unknown := len(s.unconfirmed) != 0 || (s.remote != nil && s.remote.orphan != nil && !s.remote.orphan.finished)
	busy := s.queued != nil || s.active != nil
	if s.remote != nil && (unknown || (!busy && !s.remote.ready && !s.remote.attaching)) {
		return errors.New("远端状态未同步或结果未知，请接管后使用恢复远端接入")
	}
	if s.queued != nil || s.active != nil || len(s.unconfirmed) != 0 || (s.remote != nil && s.remote.attaching) {
		return errors.New("命令仍在执行或状态未知，请等待终端空闲后再交还")
	}
	s.stopIdle()
	s.setControl(ControlHuman)
	return nil
}

// stopIdle、startIdle 和 armIdle 均由持有 s.mu 的调用方调用。
func (s *Session) stopIdle() {
	s.idleVersion++
	if s.idleCancel != nil {
		s.idleCancel()
		s.idleCancel = nil
	}
	s.idleStarted = time.Time{}
}

func (s *Session) startIdle() {
	s.stopIdle()
	if s.owner != ControlAgent {
		return
	}
	s.idleStarted = s.nowIdle()
	s.armIdle()
}

func (s *Session) nowIdle() time.Time {
	if s.idleNow != nil {
		return s.idleNow()
	}
	return time.Now()
}

func (s *Session) armIdle() {
	if s.owner != ControlAgent || s.idleStarted.IsZero() || s.idleCancel != nil || s.closed {
		return
	}
	delay := max(0, controlIdleTimeout-s.nowIdle().Sub(s.idleStarted))
	version := s.idleVersion
	expire := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.owner != ControlAgent || version != s.idleVersion || s.closed {
			return
		}
		s.idleCancel = nil
		if !s.idleReady() {
			return
		}
		s.stopIdle()
		s.setControl(ControlHuman)
	}
	if s.idleAfter != nil {
		s.idleCancel = s.idleAfter(delay, expire)
	} else {
		timer := time.AfterFunc(delay, expire)
		s.idleCancel = func() { timer.Stop() }
	}
}

func (s *Session) idleReady() bool {
	if s.queued != nil || s.active != nil || len(s.unconfirmed) != 0 {
		return false
	}
	if r := s.remote; r != nil {
		return !r.attaching && r.ready && len(r.idleOutput) == 0 && !r.dirty && r.pending == 0 && r.orphan == nil
	}
	return s.ready && !s.dirty && s.pending == 0
}

// markUnknown 和 confirmFinished 由持有 s.mu 的调用方调用。
func (s *Session) markUnknown(r *request) {
	if s.unconfirmed == nil {
		s.unconfirmed = make(map[*request]struct{})
	}
	s.unconfirmed[r] = struct{}{}
}

func (s *Session) confirmFinished(r *request) {
	delete(s.unconfirmed, r)
}

// blocksHumanInput 由持有 s.mu 的调用方调用。
func (s *Session) blocksHumanInput() bool {
	return s.owner == ControlAgent || (s.owner != ControlHumanTakeover &&
		(s.active != nil || (s.remote != nil && s.remote.attaching && s.remote.injected)))
}

// humanForegroundInput 由持有 s.mu 的调用方调用。
func (s *Session) humanForegroundInput() bool {
	if s.owner == ControlHumanTakeover &&
		((s.active != nil && !s.active.finished) || (s.remote != nil && s.remote.attaching && s.remote.injected)) {
		return true
	}
	if s.remote != nil || s.pending == 0 || s.command == nil || s.command.Process == nil || s.pty == nil {
		return false
	}
	group, err := s.foregroundGroup()
	return err == nil && group > 1 && group != s.command.Process.Pid
}

// SessionDiagnostic 返回只读控制和远端接入状态。
func (s *Session) SessionDiagnostic() control.Diagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner := "human"
	if s.owner == ControlAgent {
		owner = "agent"
	} else if s.owner == ControlHumanTakeover {
		owner = "human_takeover"
	}
	state := "none"
	if s.remote != nil {
		state = "not_ready"
		if s.remote.attaching {
			state = "attaching"
		} else if s.remote.ready {
			state = "ready"
		}
	}
	currentUnknown := len(s.unconfirmed) != 0 || (s.remote != nil && s.remote.orphan != nil && !s.remote.orphan.finished)
	unknown := s.unknownHistory || currentUnknown
	action := "none"
	busy := s.queued != nil || s.active != nil
	if s.remote != nil && (currentUnknown || (!busy && !s.remote.ready && !s.remote.attaching)) {
		action = "接管终端并使用恢复远端接入"
	} else if busy {
		action = "等待当前命令完成"
	}
	return control.Diagnostic{Control: owner, Remote: state, Unknown: unknown, SuggestedAction: action, Available: true}
}

// CanShowRemoteRecovery 表示人类接管后可以申请恢复远端目标。
func (s *Session) CanShowRemoteRecovery() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.owner == ControlHumanTakeover && s.remote != nil && !s.remote.attaching
}
