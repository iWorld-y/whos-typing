// 本文件管理手动接入的远端 Bash 会话。
package shell

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type remoteSession struct {
	token      string
	marker     []byte
	bootstrap  string
	input      []byte
	attachDone chan error
	attaching  bool
	installed  bool
	ready      bool
	pending    int
	dirty      bool
	line       []byte
	lineEdited bool
	exit       bool
	orphan     *request
	prompt     string
	idleOutput []byte
	group      int
	keys       uint64
	changed    bool
	auto       []byte
	autoCtx    context.Context
	injected   bool
	injecting  bool
	resetWrite bool
	echo       []byte
	echoDone   bool
}

// Attach 等待用户在远端 Bash 中手动执行接入命令；它不会写入终端。
func (s *Session) Attach(ctx context.Context, shellName string, show func(string) error) error {
	if shellName != "bash" {
		return errors.New("目前只支持接入远端 Bash")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("生成远端会话标识失败: %w", err)
	}
	r := &remoteSession{token: hex.EncodeToString(nonce[:]), attaching: true, attachDone: make(chan error, 1)}
	r.marker = []byte("\x1eWT:" + r.token + ":")
	r.bootstrap = remoteBootstrap(r.token)
	s.mu.Lock()
	if s.closed || s.send == nil {
		s.mu.Unlock()
		return errors.New("终端会话尚未启动或已经结束")
	}
	if s.active != nil || s.queued != nil || (s.remote != nil && (s.remote.attaching || s.remote.ready)) {
		s.mu.Unlock()
		return errors.New("终端正在执行命令或远端已接入")
	}
	group, err := s.foregroundGroup()
	if err != nil || group <= 1 || group == s.command.Process.Pid {
		s.mu.Unlock()
		return errors.New("无法确认外层 Shell 正在运行远端连接")
	}
	r.group = group
	r.keys = s.keys
	if s.remote != nil {
		s.retired = append(s.retired, s.remote)
	}
	s.remote = r
	s.mu.Unlock()
	if err := show(r.bootstrap); err != nil {
		s.cancelAttach(r)
		return err
	}
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	select {
	case err := <-r.attachDone:
		return err
	case <-ctx.Done():
		sent := s.cancelAttach(r)
		if sent {
			return errors.New("远端接入已取消，接入命令可能已执行，请确认远端状态")
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("远端接入超时，请重新接入")
		}
		return errors.New("远端接入已取消")
	case <-timer.C:
		if s.cancelAttach(r) {
			return errors.New("远端接入超时，接入命令可能已执行，请确认远端状态")
		}
		return errors.New("远端接入超时，请重新接入")
	}
}

// AttachAuto 等待界面确认后，将接入命令排入终端输入队列。
func (s *Session) AttachAuto(ctx context.Context, shellName string, confirm func(context.Context) error) error {
	if confirm == nil {
		return errors.New("远端接入需要窗口确认")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err := s.Attach(ctx, shellName, func(script string) error {
		if err := confirm(ctx); err != nil {
			return err
		}
		return s.queueAttach(ctx, script)
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("远端接入超时，请重新确认")
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("远端接入已取消")
	}
	return err
}

func (s *Session) queueAttach(ctx context.Context, script string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.remote
	if r == nil || !r.attaching || r.bootstrap != script || s.closed || s.send == nil {
		return errors.New("远端接入已经结束")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkAutoAttach(r); err != nil {
		return err
	}
	r.autoCtx = ctx
	r.auto = []byte("\x00WT-ATTACH:" + s.token + ":" + r.token)
	s.send(r.auto)
	return nil
}

// checkAutoAttach 由持有 s.mu 的调用方调用。
func (s *Session) checkAutoAttach(r *remoteSession) error {
	if len(r.input) != 0 || r.changed || s.dirty || s.keys != r.keys || s.keys > s.submitted {
		return errors.New("确认期间终端状态发生变化，请重新确认远端提示符")
	}
	group, err := s.foregroundGroup()
	if err != nil || group != r.group {
		return errors.New("远端连接状态发生变化，请重新接入")
	}
	return nil
}

func (s *Session) foregroundGroup() (int, error) {
	raw, err := s.pty.SyscallConn()
	if err != nil {
		return 0, err
	}
	var group int32
	var errno syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&group)))
	}); err != nil {
		return 0, err
	}
	if errno != 0 {
		return 0, errno
	}
	return int(group), nil
}

func (s *Session) cancelAttach(r *remoteSession) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remote == r {
		if r.injecting {
			r.resetWrite = true
			_ = s.pty.SetWriteDeadline(time.Now())
		}
		if r.injected {
			s.flushAttachEcho(r)
			s.state(false)
			s.reportBlockedInput()
		}
		s.retired = append(s.retired, r)
		s.remote = nil
	}
	return r.injected
}

func remoteQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "'\\''") + "'"
}

func remoteBootstrap(token string) string {
	mark := "\\036WT:" + token + ":"
	prompt := "__wt_prompt_" + token
	emit := "__wt_emit_" + token
	generation := "__wt_generation_" + token
	begin := "__wt_begin_" + token
	restore := "__wt_restore_" + token
	end := "__wt_end_" + token
	previous := "__wt_previous_" + token
	data := "__wt_data_" + token
	check := "__wt_check_" + token
	attr := "__wt_attr_" + token
	variable := "__wt_variable_" + token
	script := strings.Join([]string{
		`if [ -z "${BASH_VERSION-}" ] || [[ $- != *i* ]] || ! shopt -q promptvars; then builtin printf '{{MARK}}F\037';`,
		`else {{CHECK}}=0; for {{VARIABLE}} in PS1 PROMPT_COMMAND; do`,
		`{{ATTR}}=$(declare -p "${{{VARIABLE}}}" 2>/dev/null || :); {{ATTR}}="${{{ATTR}}#declare -}"; {{ATTR}}="${{{ATTR}}%% *}";`,
		`case "${{{ATTR}}}" in *r*) {{CHECK}}=1 ;; esac; done;`,
		`if [ "${{{CHECK}}}" = 1 ]; then unset {{CHECK}} {{ATTR}} {{VARIABLE}}; builtin printf '{{MARK}}F\037';`,
		`else unset {{CHECK}} {{ATTR}} {{VARIABLE}}; {{GEN}}=0;`,
		`{{EMIT}}(){ builtin printf '{{MARK}}P:%s\037' "${{{GEN}}}"; };`,
		`{{PROMPT}}(){ local s=$? a p; a=$(declare -p PS1 2>/dev/null || :); a="${a#declare -}"; a="${a%% *}";`,
		`case "$a" in *r*) builtin printf '{{MARK}}F\037'; return "$s" ;; esac;`,
		`p="${PS1-}"; p=${p//'\[$({{EMIT}})\]'/};`,
		`if ! shopt -q promptvars; then PS1="$p"; builtin printf '{{MARK}}F\037'; return "$s"; fi;`,
		`{{GEN}}=$(( ${{{GEN}}:-0} + 1 )); builtin printf '{{MARK}}C:%s\037' "${{{GEN}}}";`,
		`PS1="${p}"'\[$({{EMIT}})\]'; return "$s"; };`,
		`{{BEGIN}}(){ {{PREVIOUS}}=$?; builtin printf '{{MARK}}S\037'; return "${{{PREVIOUS}}}"; };`,
		`{{RESTORE}}(){ return "${{{PREVIOUS}}}"; };`,
		`{{END}}(){ local s=$1; unset {{DATA}} {{PREVIOUS}}; builtin printf '{{MARK}}E:%s\037' "$s"; return "$s"; };`,
		`{{ATTR}}=$(declare -p PROMPT_COMMAND 2>/dev/null || :); {{ATTR}}="${{{ATTR}}#declare -}"; {{ATTR}}="${{{ATTR}}%% *}";`,
		`case "${{{ATTR}}}" in *a*) if (( BASH_VERSINFO[0] > 5 || (BASH_VERSINFO[0] == 5 && BASH_VERSINFO[1] >= 1) )); then PROMPT_COMMAND+=({{PROMPT}});`,
		`else PROMPT_COMMAND[0]="${PROMPT_COMMAND[0]}"$'\n'{{PROMPT}}; fi ;;`,
		`*) PROMPT_COMMAND="${PROMPT_COMMAND}"$'\n'{{PROMPT}} ;; esac;`,
		`unset {{ATTR}}; builtin printf '{{MARK}}I\037'; fi; fi`,
	}, " ")
	script = strings.NewReplacer(
		"{{MARK}}", mark, "{{PROMPT}}", prompt, "{{EMIT}}", emit,
		"{{GEN}}", generation, "{{BEGIN}}", begin, "{{RESTORE}}", restore,
		"{{END}}", end, "{{PREVIOUS}}", previous, "{{DATA}}", data,
		"{{CHECK}}", check, "{{ATTR}}", attr, "{{VARIABLE}}", variable,
	).Replace(script)
	return "eval " + remoteQuote(script)
}

func remoteCommand(token, command string) (string, error) {
	if strings.IndexByte(command, 0) >= 0 {
		return "", errors.New("远端命令不能包含 NUL 字节")
	}
	var encoded strings.Builder
	for _, b := range []byte(command) {
		encoded.WriteString(`\0`)
		encoded.WriteString(fmt.Sprintf("%03o", b))
	}
	data := "__wt_data_" + token
	return fmt.Sprintf(`__wt_begin_%s; %s="$(builtin printf '%%b_' '%s')"; __wt_restore_%s; eval "${%s%%_}"; __wt_end_%s "$?"`+"\r", token, data, encoded.String(), token, data, token), nil
}

func (r *remoteSession) attachInputMatches() bool {
	p := bytes.ReplaceAll(r.input, []byte("\x1b[200~"), nil)
	p = bytes.ReplaceAll(p, []byte("\x1b[201~"), nil)
	return bytes.Equal(p, []byte(r.bootstrap+"\r")) || bytes.Equal(p, []byte(r.bootstrap+"\n")) || bytes.Equal(p, []byte(r.bootstrap+"\r\n"))
}

// attachInputError 区分粘贴内容不匹配和仍有未提交按键，不记录输入内容。
func (r *remoteSession) attachInputError(s *Session) error {
	if !r.attachInputMatches() {
		input := bytes.ReplaceAll(r.input, []byte("\x1b[200~"), nil)
		input = bytes.ReplaceAll(input, []byte("\x1b[201~"), nil)
		expected := []byte(r.bootstrap + "\r")
		pos := 0
		for pos < len(input) && pos < len(expected) && input[pos] == expected[pos] {
			pos++
		}
		return fmt.Errorf("接入期间有其他输入或接入命令不完整（输入 %d 字节，期望 %d 字节，首处差异位置 %d）；请在空提示符重新接入", len(input), len(expected), pos+1)
	}
	if s.keys > s.submitted {
		return errors.New("接入期间有其他输入或按键尚未提交，请在空提示符重新接入")
	}
	return nil
}

func (r *remoteSession) trackInput(s *Session, p []byte) {
	if match := kittyKey.FindSubmatch(p); len(match) != 0 {
		code, _ := strconv.Atoi(string(match[1]))
		mods := 1
		if len(match[2]) != 0 {
			mods, _ = strconv.Atoi(string(match[2]))
		}
		if code == 13 {
			r.submit(s)
		} else if code == 100 && (mods-1)&4 != 0 {
			r.exit = !r.dirty && r.pending == 0
			r.pending++
		} else {
			r.dirty = true
			if mods == 1 && code >= 32 && code <= 126 {
				r.line = append(r.line, byte(code))
			} else {
				r.lineEdited = true
			}
		}
		return
	}
	for _, b := range p {
		switch b {
		case '\r', '\n':
			r.submit(s)
		case 3:
			r.pending++
			r.dirty = s.keys > s.submitted
			r.lineEdited = true
		case 4:
			r.exit = !r.dirty && r.pending == 0
			r.pending++
		default:
			r.dirty = true
			if b >= 32 && b <= 126 {
				r.line = append(r.line, b)
			} else {
				r.lineEdited = true
			}
		}
	}
}

func (r *remoteSession) submit(s *Session) {
	fields := strings.Fields(string(r.line))
	r.exit = !r.lineEdited && len(fields) > 0 && len(fields) <= 2 && (fields[0] == "exit" || fields[0] == "logout")
	r.pending++
	r.dirty = s.keys > s.submitted
	r.line = nil
	r.lineEdited = false
}

// observeOutput 在就绪后发现非提示符控制输出时，将远端状态置为未知。
func (r *remoteSession) observeOutput(p []byte) {
	allowed := [][]byte{[]byte("\x1b[?2004h"), []byte("\x1b[K")}
	for _, b := range p {
		r.idleOutput = append(r.idleOutput, b)
		matches := false
		for _, sequence := range allowed {
			if bytes.Equal(r.idleOutput, sequence) {
				r.idleOutput = nil
				matches = true
				break
			}
			matches = matches || bytes.HasPrefix(sequence, r.idleOutput)
		}
		if !matches {
			r.idleOutput = nil
			r.ready = false
			return
		}
	}
}

func (s *Session) controlRemote(remote *remoteSession, mark string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.remote != remote {
		return true
	}
	switch {
	case mark == "I":
		if remote.attaching {
			s.flushAttachEcho(remote)
			if err := remote.attachInputError(s); err != nil {
				s.failAttach(remote, err)
				return true
			}
			remote.installed = true
		}
		return true
	case mark == "F":
		if remote.attaching {
			s.flushAttachEcho(remote)
			s.failAttach(remote, errors.New("目标不是可交互的 Bash，或 promptvars 已关闭，或 PS1、PROMPT_COMMAND 不可修改"))
		} else {
			remote.prompt = ""
			remote.ready = false
		}
		return true
	case strings.HasPrefix(mark, "C:"):
		generation := strings.TrimPrefix(mark, "C:")
		if n, err := strconv.ParseUint(generation, 10, 64); err == nil && n > 0 {
			remote.prompt = generation
		}
		return true
	case strings.HasPrefix(mark, "P:"):
		generation := strings.TrimPrefix(mark, "P:")
		if remote.prompt == "" || remote.prompt != generation {
			return true
		}
		remote.prompt = ""
		remote.idleOutput = nil
		if remote.attaching {
			if !remote.installed {
				return true
			}
			if err := remote.attachInputError(s); err != nil {
				s.failAttach(remote, err)
				return true
			}
			remote.attaching = false
			remote.ready = true
			remote.input = nil
			if remote.injected {
				s.state(false)
				s.reportBlockedInput()
			}
			remote.attachDone <- nil
			return true
		}
		if r := s.active; r != nil && r.remote {
			s.active = nil
			s.state(false)
			if r.finished {
				remote.ready = remote.pending == 0 && !remote.dirty
				s.finish(r, r.exitCode, r.err)
			} else {
				s.captureOutput(r, nil, true)
				remote.ready = false
				s.finish(r, 1, errors.New("远端返回提示符，但未报告命令完成状态"))
			}
			s.reportBlockedInput()
		} else if remote.orphan != nil {
			r := remote.orphan
			remote.orphan = nil
			remote.ready = r.finished && remote.pending == 0 && !remote.dirty
		} else if remote.pending > 0 {
			remote.pending--
			remote.ready = remote.pending == 0 && !remote.dirty
			if remote.pending == 0 {
				remote.exit = false
			}
		}
		return true
	case mark == "S":
		if r := s.active; r != nil && r.remote {
			command := strings.ReplaceAll(r.command, "\n", "\r\n")
			s.parsed = append(s.parsed, []byte("\r\n[CLI] "+command+"\r\n")...)
			r.started = true
		} else if remote.orphan != nil {
			remote.orphan.started = true
		}
		return true
	case strings.HasPrefix(mark, "E:"):
		code, err := strconv.Atoi(strings.TrimPrefix(mark, "E:"))
		if err != nil || code < 0 || code > 255 {
			return false
		}
		if r := s.active; r != nil && r.remote && r.started {
			s.captureOutput(r, nil, true)
			r.finished = true
			r.exitCode = code
			time.AfterFunc(2*time.Second, func() { s.finishWithoutPrompt(r) })
		} else if remote.orphan != nil && remote.orphan.started {
			remote.orphan.finished = true
		}
		return true
	}
	return false
}

// failAttach 由持有 s.mu 的调用方调用。
func (s *Session) failAttach(remote *remoteSession, err error) {
	remote.attaching = false
	if remote.injected {
		s.flushAttachEcho(remote)
		s.state(false)
		s.reportBlockedInput()
	}
	s.retired = append(s.retired, remote)
	s.remote = nil
	remote.attachDone <- err
}

// remoteDisconnected 在本地 Shell 提示符出现时结束远端状态。
func (s *Session) remoteDisconnected() {
	remote := s.remote
	if remote.attaching {
		s.failAttach(remote, errors.New("远端连接已结束，接入未完成"))
	} else {
		if r := s.queued; r != nil && r.remote {
			s.queued = nil
			s.finish(r, 1, errors.New("远端连接已结束"))
		}
		if r := s.active; r != nil && r.remote {
			if r.injecting {
				r.resetWriteDeadline = true
				_ = s.pty.SetWriteDeadline(time.Now())
			}
			s.captureOutput(r, nil, true)
			s.active = nil
			s.state(false)
			s.finish(r, 1, errors.New("远端连接已结束，命令结果未知"))
		}
		s.retired = append(s.retired, remote)
		s.remote = nil
	}
	s.pending = 0
	if !remote.dirty && (remote.pending == 0 || remote.exit) {
		s.dirty = false
		s.ready = true
	} else {
		s.dirty = true
		s.ready = false
	}
}
