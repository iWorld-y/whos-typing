# 受管 SSH 会话的远端执行组件：为 Bash 分配控制终端，
# 用独立 FD 接收命令、报告状态，并把 PTY 输出与状态分帧复用到 stdout。
# 仅依赖 Python 3 标准库、Bash 和 base64；随会话临时启动，退出后回收。
from __future__ import annotations

import base64
import errno
import fcntl
import json
import os
import pty
import select
import shutil
import signal
import struct
import sys
import termios
import time
from typing import NoReturn

DRIVER = r"""
set -m
trap ':' INT
PS1='wt$ '
__wt_enc() { printf '%s' "$1" | base64 | tr -d '\n'; }
__wt_pwd() { __wt_enc "$PWD"; }
printf 'META %s %s %s\n' "$(__wt_enc "${USER:-$(id -un)}")" "$(__wt_enc "$(hostname)")" "$(__wt_pwd)" >&4
printf 'READY %s\n' "$(__wt_pwd)" >&4
__wt_mode=agent
while :; do
  if [ "$__wt_mode" = human ]; then
    printf '%s' "$PS1"
    printf 'HPROMPT\n' >&4
    # 轮询读终端行；规范模式下未提交输入留在内核行缓冲，超时不丢失。
    while :; do
      IFS= read -r -t 1 __wt_line
      __wt_rs=$?
      if [ "$__wt_rs" -eq 0 ]; then break; fi
      # bash 3.2 超时返回 1，4+ 返回 >128；都继续轮询。
      # 终端失效时由组件负责终止 Bash。
      if IFS= read -r -u 3 -t 1 __wt_req && [ "$__wt_req" = R ]; then
        __wt_mode=agent
        break
      fi
    done
    if [ "$__wt_mode" = agent ]; then
      printf 'READY %s\n' "$(__wt_pwd)" >&4
      continue
    fi
    [ -n "$__wt_line" ] || { printf 'HDONE 0 %s\n' "$(__wt_pwd)" >&4; continue; }
    printf 'HRUN\n' >&4
    eval "$__wt_line"
    printf 'HDONE %s %s\n' "$?" "$(__wt_pwd)" >&4
  else
    IFS= read -r -u 3 __wt_req
    __wt_rs=$?
    if [ "$__wt_rs" -gt 128 ]; then continue; fi
    if [ "$__wt_rs" -ne 0 ]; then exit 0; fi
    case "$__wt_req" in
      H) __wt_mode=human ;;
      E\ *)
        __wt_rest=${__wt_req#E }
        __wt_id=${__wt_rest%% *}
        __wt_cmd=$(printf '%b' "${__wt_rest#* }")
        printf 'RUN %s\n' "$__wt_id" >&4
        eval "$__wt_cmd"
        printf 'DONE %s %s %s\n' "$__wt_id" "$?" "$(__wt_pwd)" >&4
        ;;
    esac
  fi
done
"""


def frame(kind: bytes, payload: bytes) -> None:
    data = kind + struct.pack(">I", len(payload)) + payload
    sys.stdout.buffer.write(data)
    sys.stdout.buffer.flush()


def escape_command(text: str) -> str:
    return text.replace("\\", "\\\\").replace("\n", "\\n").replace("\r", "\\r")


def fail(message: str) -> NoReturn:
    frame(b"X", json.dumps({"error": message}).encode())
    sys.exit(1)


def main() -> None:
    # 登录 Shell 的初始化输出可能混在 stdout 前面；先打同步标记，后端从标记后开始解析帧。
    if len(sys.argv) > 1:
        sys.stdout.buffer.write(("WT-SYNC:" + sys.argv[1] + "\n").encode())
        sys.stdout.buffer.flush()
    if not shutil.which("bash") or not shutil.which("base64"):
        fail("远端缺少 bash 或 base64，无法启动受管会话")
    cmd_r, cmd_w = os.pipe()
    st_r, st_w = os.pipe()
    pid, master = pty.fork()
    if pid == 0:
        c = fcntl.fcntl(cmd_r, fcntl.F_DUPFD, 10)
        s = fcntl.fcntl(st_w, fcntl.F_DUPFD, 10)
        for fd in (cmd_r, cmd_w, st_r, st_w):
            if fd > 2:
                os.close(fd)
        os.dup2(c, 3)
        os.dup2(s, 4)
        os.close(c)
        os.close(s)
        env = dict(os.environ)
        env["TERM"] = "xterm-256color"
        env["WT_MANAGED"] = "1"
        env.pop("BASH_ENV", None)
        env.pop("PROMPT_COMMAND", None)
        os.execvpe("bash", ["bash", "--noprofile", "--norc", "-c", DRIVER], env)
        os._exit(127)
    os.close(cmd_r)
    os.close(st_w)
    fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
    for fd in (master, st_r, 0):
        os.set_blocking(fd, False)
    stdin_buf = b""
    status_buf = b""
    reaped: int | None = None
    closing = False
    close_started = 0.0
    escalated = 0
    drain_until: float | None = None

    def begin_close() -> None:
        nonlocal closing, close_started
        if not closing:
            closing = True
            close_started = time.monotonic()
            try:
                os.close(cmd_w)
            except OSError:
                pass

    def read_fd(fd: int, size: int) -> tuple[bytes, bool]:
        try:
            data = os.read(fd, size)
            return data, not data
        except BlockingIOError:
            return b"", False
        except OSError as e:
            if e.errno in (errno.EIO, errno.EBADF):
                return b"", True
            raise

    while True:
        now = time.monotonic()
        if reaped is None:
            try:
                done, status = os.waitpid(pid, os.WNOHANG)
            except ChildProcessError:
                done, status = pid, 0
            if done == pid:
                if os.WIFSIGNALED(status):
                    reaped = 128 + os.WTERMSIG(status)
                else:
                    reaped = os.WEXITSTATUS(status)
                drain_until = now + 0.5
        if reaped is not None and drain_until is not None and now >= drain_until:
            break
        if closing and reaped is None:
            elapsed = now - close_started
            sig = None
            if elapsed > 2.0 and escalated < 3:
                sig, escalated = signal.SIGKILL, 3
            elif elapsed > 1.5 and escalated < 2:
                sig, escalated = signal.SIGTERM, 2
            elif elapsed > 1.0 and escalated < 1:
                sig, escalated = signal.SIGHUP, 1
            if sig is not None:
                # 同时终止前台作业进程组：bash 被杀后不会转发信号。
                try:
                    fg = struct.unpack(
                        "i", fcntl.ioctl(master, termios.TIOCGPGRP, struct.pack("i", 0))
                    )[0]
                    if fg > 1:
                        os.killpg(fg, sig)
                except (OSError, struct.error):
                    pass
                try:
                    os.killpg(pid, sig)
                except (ProcessLookupError, PermissionError):
                    pass
        fds = [master, st_r]
        if not closing:
            fds.append(0)
        try:
            ready, _, _ = select.select(fds, [], [], 0.1)
        except InterruptedError:
            continue
        if 0 in ready:
            data, eof = read_fd(0, 65536)
            stdin_buf += data
            while b"\n" in stdin_buf:
                line, stdin_buf = stdin_buf.split(b"\n", 1)
                if not line.strip():
                    continue
                try:
                    msg = json.loads(line)
                except ValueError:
                    continue
                kind = msg.get("t")
                try:
                    if kind == "exec":
                        payload = "E {} {}\n".format(
                            msg["id"], escape_command(msg["cmd"])
                        )
                        os.write(cmd_w, payload.encode())
                    elif kind == "human":
                        os.write(cmd_w, b"H\n")
                    elif kind == "keyboard":
                        os.write(master, base64.b64decode(msg["data"]))
                    elif kind == "paste":
                        # 完整写入后才回执；非阻塞 master 可能短写或 EAGAIN。
                        view = memoryview(base64.b64decode(msg["data"]))
                        ok = True
                        deadline = time.monotonic() + 5
                        while view:
                            try:
                                n = os.write(master, view)
                                view = view[n:]
                            except BlockingIOError:
                                remain = deadline - time.monotonic()
                                if remain <= 0:
                                    ok = False
                                    break
                                _, writable, _ = select.select([], [master], [], remain)
                                if not writable:
                                    ok = False
                                    break
                            except OSError:
                                ok = False
                                break
                        frame(
                            b"S",
                            (("POK " if ok else "PFAIL ") + str(msg["id"])).encode(),
                        )
                    elif kind == "release":
                        os.write(cmd_w, b"R\n")
                    elif kind == "resize":
                        rows = max(1, min(int(msg.get("rows", 24)), 65535))
                        cols = max(1, min(int(msg.get("cols", 80)), 65535))
                        fcntl.ioctl(
                            master,
                            termios.TIOCSWINSZ,
                            struct.pack("HHHH", rows, cols, 0, 0),
                        )
                    elif kind == "close":
                        begin_close()
                except (OSError, KeyError, TypeError, ValueError):
                    begin_close()
            if eof:
                begin_close()
        if master in ready or reaped is not None:
            data, dead = read_fd(master, 65536)
            if data:
                frame(b"O", data)
            if dead and reaped is None:
                begin_close()
                drain_until = time.monotonic() + 0.5
        if st_r in ready or reaped is not None:
            data, dead = read_fd(st_r, 65536)
            status_buf += data
            while b"\n" in status_buf:
                line, status_buf = status_buf.split(b"\n", 1)
                if line.strip():
                    # 先发完已到达的 PTY 输出，保证 DONE 前的最后一段输出计入结果。
                    while True:
                        pending, _ = read_fd(master, 65536)
                        if not pending:
                            break
                        frame(b"O", pending)
                    frame(b"S", line)
            if dead and reaped is None and not closing:
                begin_close()
        if reaped is not None and drain_until is None:
            drain_until = time.monotonic() + 0.5
    # 收尾：尽力消费剩余输出，再报告 Bash 退出状态。
    deadline = time.monotonic() + 0.3
    while time.monotonic() < deadline:
        data, dead = read_fd(master, 65536)
        if data:
            frame(b"O", data)
        sdata, _ = read_fd(st_r, 65536)
        status_buf += sdata
        if dead and not sdata:
            break
        if not data and not sdata:
            break
        time.sleep(0.02)
    while b"\n" in status_buf:
        line, status_buf = status_buf.split(b"\n", 1)
        if line.strip():
            frame(b"S", line)
    try:
        os.close(master)
    except OSError:
        pass
    frame(b"X", json.dumps({"exit": reaped if reaped is not None else -1}).encode())


if __name__ == "__main__":
    try:
        main()
    except BrokenPipeError:
        sys.exit(0)
    except KeyboardInterrupt:
        sys.exit(130)
