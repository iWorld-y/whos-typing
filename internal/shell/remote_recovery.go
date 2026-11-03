// 本文件提供人类确认后的远端重新接入，不开放 CLI 绕过接管保护。
package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrRemoteNotReady 表示远端状态需要由用户确认后重新接入。
var ErrRemoteNotReady = errors.New("远端状态未同步，请确认 Bash 空提示符后重新接入")

// RecoverRemote 仅供界面主动恢复；确认、输入校验及握手完成前保留人类控制权。
func (s *Session) RecoverRemote(ctx context.Context, confirm func(context.Context) error) error {
	if err := s.attachAuto(ctx, "bash", confirm, true); err != nil {
		return err
	}
	return s.ReturnControl()
}

// canRecoverRemote 由持有 s.mu 的调用方调用，不把未知结果当作已完成。
func (s *Session) canRecoverRemote() error {
	if s.owner != ControlHumanTakeover || s.remote == nil {
		return errors.New("只有人类接管的远端会话可以重新接入")
	}
	if s.active != nil || s.queued != nil || s.remote.attaching || s.remote.orphan != nil || len(s.unconfirmed) != 0 {
		return errors.New("命令仍在执行或结果未知，无法重新接入")
	}
	if s.remote.dirty || s.remote.pasting || len(s.remote.pasteMark) != 0 || s.keys > s.submitted {
		return errors.New("请先完成或取消当前输入，再重新接入")
	}
	return nil
}

// remoteCleanup 只移除本窗口先前安装的钩子，保留用户原有提示符命令。
func remoteCleanup(tokens []string) string {
	var script strings.Builder
	for _, token := range tokens {
		prompt, emit := "__wt_prompt_"+token, "__wt_emit_"+token
		index := "__wt_cleanup_" + token
		fmt.Fprintf(&script, `for %[1]s in "${!PROMPT_COMMAND[@]}"; do if [[ "${PROMPT_COMMAND[$%[1]s]}" == %[2]s ]]; then unset 'PROMPT_COMMAND[%[1]s]'; else PROMPT_COMMAND[$%[1]s]="${PROMPT_COMMAND[$%[1]s]//$'\n'%[2]s/}"; fi; done; unset %[1]s; `, index, prompt)
		fmt.Fprintf(&script, `PS1=${PS1//'\[$(%s)\]'/}; unset -f %s %s __wt_begin_%s __wt_restore_%s __wt_end_%s; unset __wt_generation_%s __wt_previous_%s __wt_data_%s; `, emit, prompt, emit, token, token, token, token, token, token)
	}
	return script.String()
}
