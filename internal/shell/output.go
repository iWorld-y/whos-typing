// 本文件解析终端字节流，并分发窗口和 CLI 输出。
package shell

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"whos-typing/internal/control"
)

// Read 转发原始终端输出，并从字节流中识别和移除本会话标记。
func (s *Session) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		if len(s.parsed) != 0 {
			n := copy(p, s.parsed)
			s.parsed = s.parsed[n:]
			s.mu.Unlock()
			return n, nil
		}
		s.mu.Unlock()
		buf := make([]byte, 64<<10)
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.readBuf = append(s.readBuf, buf[:n]...)
			s.parse(false)
		}
		if err != nil {
			s.parse(true)
			s.onEnd()
			s.mu.Lock()
			if len(s.parsed) != 0 {
				n := copy(p, s.parsed)
				s.parsed = s.parsed[n:]
				s.mu.Unlock()
				return n, nil
			}
			s.mu.Unlock()
			return 0, io.EOF
		}
	}
}

func (s *Session) parse(flush bool) {
	for len(s.readBuf) != 0 {
		markers := s.markers()
		pos := -1
		var selected markerRef
		for _, marker := range markers {
			index := bytes.Index(s.readBuf, marker.prefix)
			if index >= 0 && (pos < 0 || index < pos) {
				pos, selected = index, marker
			}
		}
		if pos < 0 {
			keep := 0
			if !flush {
				for _, marker := range markers {
					for n := min(len(s.readBuf), len(marker.prefix)-1); n > keep; n-- {
						if bytes.Equal(s.readBuf[len(s.readBuf)-n:], marker.prefix[:n]) {
							keep = n
							break
						}
					}
				}
			}
			s.emit(s.readBuf[:len(s.readBuf)-keep])
			s.readBuf = s.readBuf[len(s.readBuf)-keep:]
			return
		}
		if pos > 0 {
			s.emit(s.readBuf[:pos])
			s.readBuf = s.readBuf[pos:]
		}
		end := bytes.IndexByte(s.readBuf[len(selected.prefix):], 0x1f)
		if end < 0 {
			if !flush && len(s.readBuf) < len(selected.prefix)+64 {
				return
			}
			s.emit(s.readBuf[:1])
			s.readBuf = s.readBuf[1:]
			continue
		}
		end += len(selected.prefix)
		marker := string(s.readBuf[len(selected.prefix):end])
		accepted := false
		if selected.remote == nil {
			accepted = s.control(marker)
		} else {
			accepted = s.controlRemote(selected.remote, marker)
		}
		if !accepted {
			s.emit(s.readBuf[:end+1])
		}
		s.readBuf = s.readBuf[end+1:]
	}
}

type markerRef struct {
	prefix []byte
	remote *remoteSession
}

func (s *Session) markers() []markerRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	markers := []markerRef{{prefix: s.marker}}
	if s.remote != nil {
		markers = append(markers, markerRef{prefix: s.remote.marker, remote: s.remote})
	}
	for _, remote := range s.retired {
		markers = append(markers, markerRef{prefix: remote.marker, remote: remote})
	}
	return markers
}

func (s *Session) emit(p []byte) {
	if len(p) == 0 {
		return
	}
	s.mu.Lock()
	if !s.initSeen {
		p = s.filterInitEcho(p, false)
	}
	if remote := s.remote; remote != nil && remote.attaching && !remote.injected && len(p) != 0 {
		remote.changed = true
	}
	if remote := s.remote; remote != nil && remote.attaching && remote.injected && !remote.echoDone {
		p = remote.filterAttachEcho(p, false)
	}
	if remote := s.remote; remote != nil && remote.ready && s.active == nil {
		remote.observeOutput(p)
	}
	if s.active != nil && !s.active.started {
		s.parsed = append(s.parsed, terminalControl(s.active, p)...)
		s.mu.Unlock()
		return
	}
	s.parsed = append(s.parsed, p...)
	if r := s.active; r != nil && r.started && !r.finished && !r.abandoned && r.err == nil {
		s.captureOutput(r, p, false)
	}
	s.mu.Unlock()
}

// terminalControl 保留开始标记之前的终端控制序列，隐藏包装命令文本。
func terminalControl(r *request, p []byte) []byte {
	r.prefix = append(r.prefix, p...)
	var out []byte
	for len(r.prefix) > 0 {
		if r.prefix[0] != 0x1b {
			if r.prefix[0] < 0x20 || r.prefix[0] == 0x7f {
				out = append(out, r.prefix[0])
			}
			r.prefix = r.prefix[1:]
			continue
		}
		if len(r.prefix) < 2 {
			break
		}
		end := -1
		switch r.prefix[1] {
		case '[':
			for i := 2; i < len(r.prefix); i++ {
				if r.prefix[i] >= 0x40 && r.prefix[i] <= 0x7e {
					end = i + 1
					break
				}
			}
		case ']', 'P', '_', '^':
			for i := 2; i < len(r.prefix); i++ {
				if r.prefix[i] == '\a' {
					end = i + 1
					break
				}
				if r.prefix[i] == 0x1b && i+1 < len(r.prefix) && r.prefix[i+1] == '\\' {
					end = i + 2
					break
				}
			}
		default:
			end = 2
		}
		if end < 0 {
			if len(r.prefix) < 64<<10 {
				break
			}
			r.prefix = r.prefix[1:]
			continue
		}
		sequence := r.prefix[:end]
		if !bytes.HasPrefix(sequence, []byte("\x1b]0;")) && !bytes.HasPrefix(sequence, []byte("\x1b]1;")) && !bytes.HasPrefix(sequence, []byte("\x1b]2;")) {
			out = append(out, sequence...)
		}
		r.prefix = r.prefix[end:]
	}
	return out
}

func (s *Session) control(mark string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case mark == "I":
		if !s.initSeen {
			s.parsed = append(s.parsed, s.filterInitEcho(nil, true)...)
			s.initSeen = true
		}
		return true
	case mark == "P":
		if s.remote != nil {
			s.remoteDisconnected()
			return true
		}
		if !s.initial {
			s.initial = true
		} else if s.active != nil {
			r := s.active
			s.active = nil
			s.state(false)
			if r.finished {
				s.ready = s.pending == 0 && !s.dirty
				s.finish(r, r.exitCode, r.err)
			} else {
				s.captureOutput(r, nil, true)
				s.finish(r, 1, errors.New("Shell 返回提示符，但未报告命令完成状态"))
				s.startIdle()
			}
			s.confirmFinished(r)
			s.reportBlockedInput()
		} else if s.localOrphan != nil {
			s.confirmFinished(s.localOrphan)
			s.localOrphan = nil
			s.startIdle()
		} else if s.pending > 0 {
			s.pending--
		}
		if s.inputBlocked {
			s.reportBlockedInput()
		}
		s.ready = s.pending == 0 && !s.dirty
		s.armIdle()
		return true
	case mark == "S":
		if s.active != nil {
			command := strings.ReplaceAll(s.active.command, "\n", "\r\n")
			s.parsed = append(s.parsed, []byte("\r\n[CLI] "+command+"\r\n")...)
			s.active.started = true
		} else if s.localOrphan != nil {
			s.localOrphan.started = true
		}
		return true
	case strings.HasPrefix(mark, "E:"):
		code, err := strconv.Atoi(strings.TrimPrefix(mark, "E:"))
		if err != nil || code < 0 || code > 255 {
			return false
		}
		if r := s.active; r != nil && r.started {
			s.captureOutput(r, nil, true)
			r.finished = true
			r.exitCode = code
			s.confirmFinished(r)
			s.startIdle()
			time.AfterFunc(2*time.Second, func() { s.finishWithoutPrompt(r) })
		} else if r := s.localOrphan; r != nil && r.started {
			r.finished = true
			s.confirmFinished(r)
			s.startIdle()
		}
		return true
	}
	return false
}

// 结束标记已经证明命令完成；提示符不再触发时返回结果并继续拒绝新命令。
func (s *Session) finishWithoutPrompt(r *request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != r || !r.finished {
		return
	}
	s.active = nil
	if r.remote && s.remote != nil {
		s.remote.ready = false
		s.remote.orphan = r
	} else {
		s.ready = false
	}
	s.state(false)
	s.finish(r, r.exitCode, r.err)
}

// filterInitEcho 只移除注入的初始化命令回显，保留其他启动输出。
func (s *Session) filterInitEcho(p []byte, flush bool) []byte {
	s.initScan = append(s.initScan, p...)
	var out []byte
	for len(s.initScan) != 0 {
		pos := bytes.IndexByte(s.initScan, 's')
		if pos < 0 {
			out = append(out, s.initScan...)
			s.initScan = nil
			break
		}
		out = append(out, s.initScan[:pos]...)
		s.initScan = s.initScan[pos:]
		consumed, incomplete := matchEcho(s.initScan, s.initLine)
		if consumed > 0 {
			s.initScan = s.initScan[consumed:]
		} else if incomplete && !flush {
			break
		} else {
			out = append(out, s.initScan[0])
			s.initScan = s.initScan[1:]
		}
	}
	return out
}

// filterAttachEcho 仅移除自动接入命令的回显，保留其他终端输出。
func (r *remoteSession) filterAttachEcho(p []byte, flush bool) []byte {
	if r.echoDone {
		return p
	}
	r.echo = append(r.echo, p...)
	var out []byte
	expected := []byte(r.bootstrap)
	if len(r.echo) > len(expected)+(64<<10) {
		flush = true
	}
	for len(r.echo) != 0 {
		pos := bytes.IndexByte(r.echo, expected[0])
		if pos < 0 {
			out = append(out, r.echo...)
			r.echo = nil
			break
		}
		out = append(out, r.echo[:pos]...)
		r.echo = r.echo[pos:]
		consumed, incomplete := matchEcho(r.echo, expected)
		if consumed > 0 {
			out = append(out, r.echo[consumed:]...)
			r.echo = nil
			r.echoDone = true
			break
		}
		if incomplete && !flush {
			break
		}
		out = append(out, r.echo[0])
		r.echo = r.echo[1:]
	}
	return out
}

// flushAttachEcho 由持有 s.mu 的调用方调用，保留无法确认的终端输出。
func (s *Session) flushAttachEcho(r *remoteSession) {
	if r.injected && !r.echoDone {
		s.parsed = append(s.parsed, r.filterAttachEcho(nil, true)...)
		r.echoDone = true
	}
}

// matchEcho 匹配 Shell 行编辑器回显，允许字符之间存在终端 CSI 序列。
func matchEcho(input, expected []byte) (int, bool) {
	i := 0
	for index, want := range expected {
		for {
			if i >= len(input) {
				return 0, true
			}
			if input[i] == '\r' || input[i] == '\b' {
				i++
				continue
			}
			if input[i] == 0x1b {
				if i+1 >= len(input) {
					return 0, true
				}
				if input[i+1] != '[' {
					break
				}
				j := i + 2
				for j < len(input) && (input[j] < 0x40 || input[j] > 0x7e) {
					j++
				}
				if j == len(input) {
					return 0, true
				}
				i = j + 1
				continue
			}
			if i+1 >= len(input) {
				return 0, true
			}
			// 行编辑器在换行重绘时会先写一个字符再退回行首。
			if index+1 < len(expected) && input[i+1] == '\r' && (input[i] == want || input[i] == ' ') {
				i += 2
				continue
			}
			break
		}
		if input[i] != want {
			return 0, false
		}
		i++
	}
	return i, false
}

var fishDirectoryMarker = []byte("\x1b]7;file://")

// captureOutput 仅从 fish 的 CLI 输出中移除完整的目录 OSC 7 序列。
func (s *Session) captureOutput(r *request, p []byte, flush bool) {
	if r.abandoned || r.err != nil {
		return
	}
	if s.kind != "fish" || r.remote {
		if _, err := r.output.Write(p); err != nil {
			r.err = fmt.Errorf("保存命令输出失败: %w", err)
		}
		return
	}
	r.capture = append(r.capture, p...)
	write := func(p []byte) {
		if r.err == nil {
			if _, err := r.output.Write(p); err != nil {
				r.err = fmt.Errorf("保存命令输出失败: %w", err)
			}
		}
	}
	for len(r.capture) > 0 {
		pos := bytes.Index(r.capture, fishDirectoryMarker)
		if pos < 0 {
			keep := 0
			if !flush {
				for n := min(len(r.capture), len(fishDirectoryMarker)-1); n > 0; n-- {
					if bytes.Equal(r.capture[len(r.capture)-n:], fishDirectoryMarker[:n]) {
						keep = n
						break
					}
				}
			}
			write(r.capture[:len(r.capture)-keep])
			r.capture = r.capture[len(r.capture)-keep:]
			return
		}
		write(r.capture[:pos])
		r.capture = r.capture[pos:]
		end := bytes.IndexByte(r.capture, '\a')
		st := bytes.Index(r.capture, []byte("\x1b\\"))
		if st >= 0 && (end < 0 || st < end) {
			end = st + 1
		}
		if end < 0 {
			if !flush && len(r.capture) < 64<<10 {
				return
			}
			write(r.capture[:1])
			r.capture = r.capture[1:]
			continue
		}
		r.capture = r.capture[end+1:]
	}
}

func (s *Session) reportBlockedInput() {
	if s.inputBlocked {
		s.parsed = append(s.parsed, []byte("\r\n[CLI 执行期间终端输入已暂停，请在完成后重新输入]\r\n")...)
		s.inputBlocked = false
	}
}

// finish 由持有 s.mu 的调用方调用。
func (s *Session) finish(r *request, code int, err error) {
	_ = os.Remove(r.source)
	if !r.abandoned {
		_, seekErr := r.output.Seek(0, io.SeekStart)
		r.result = control.Result{Output: r.output, ExitCode: code, Err: errors.Join(err, seekErr)}
	}
	close(r.done)
}
