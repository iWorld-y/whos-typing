"""受管 Bash 最小验证：真实 PTY + 独立命令/状态 FD。
所有等待都有单调时钟截止时间；退出/失败时继续消费 PTY 输出、关闭写端，
再用 waitpid(WNOHANG) 收尸。第一个用例验证超时分支本身能返回。"""

from __future__ import annotations

import json
import os
import select
import signal
import struct
import subprocess
import sys
import time
from collections.abc import Callable
from contextlib import suppress

COMPONENT = sys.argv[1] if len(sys.argv) > 1 else "component.py"
MODE = sys.argv[2] if len(sys.argv) > 2 else "local"
SSH_ARGS = json.loads(os.environ.get("WT_SSH_ARGS", "[]"))


class Timeout(Exception):
    pass


class Component:
    def __init__(self) -> None:
        if MODE == "local":
            argv = [sys.executable, COMPONENT]
        else:
            import base64

            with open(COMPONENT, "rb") as f:
                b64 = base64.b64encode(f.read()).decode()
            # 登录 Shell 可能是 fish 等，统一经 bash -c 执行；B64 仅含字母数字和 +/=。
            import secrets

            self.sync = secrets.token_hex(16)
            remote = (
                "bash -c 'python3 -c \"$(printf %s "
                + b64
                + ' | base64 --decode)" '
                + self.sync
                + "'"
            )
            argv = SSH_ARGS + [remote]
        self.proc = subprocess.Popen(
            argv,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        self.buf = b""
        self.synced = MODE == "local"
        self.output = b""
        self.status: list[str] = []
        self.exit_info: dict[str, int | str] | None = None
        self.deadline = time.monotonic() + 20

    def send(self, msg: dict[str, str | int]) -> None:
        self.proc.stdin.write((json.dumps(msg) + "\n").encode())
        self.proc.stdin.flush()

    def pump(self, timeout: float) -> None:
        end = min(time.monotonic() + timeout, self.deadline)
        while time.monotonic() < end:
            if self.proc.stdout.closed:
                return
            r, _, _ = select.select(
                [self.proc.stdout], [], [], max(0, end - time.monotonic())
            )
            if not r:
                return
            chunk = os.read(self.proc.stdout.fileno(), 65536)
            if not chunk:
                return
            self.buf += chunk
            if not self.synced:
                marker = ("WT-SYNC:" + self.sync + "\n").encode()
                idx = self.buf.find(marker)
                if idx < 0:
                    continue
                self.buf = self.buf[idx + len(marker) :]
                self.synced = True
            while len(self.buf) >= 5:
                kind = self.buf[:1]
                size = struct.unpack(">I", self.buf[1:5])[0]
                if len(self.buf) < 5 + size:
                    break
                payload = self.buf[5 : 5 + size]
                self.buf = self.buf[5 + size :]
                if kind == b"O":
                    self.output += payload
                elif kind == b"S":
                    self.status.append(payload.decode())
                elif kind == b"X":
                    self.exit_info = json.loads(payload)

    def wait_status(self, prefix: str, timeout: float = 5) -> str:
        end = time.monotonic() + timeout
        seen = 0
        while time.monotonic() < end:
            self.pump(0.05)
            while seen < len(self.status):
                line = self.status[seen]
                seen += 1
                if line.startswith(prefix):
                    return line
            if self.exit_info is not None:
                raise AssertionError(
                    f"组件提前退出: {self.exit_info!r}, 状态: {self.status!r}"
                )
        raise Timeout(f"等待状态 {prefix!r} 超时, 已有: {self.status!r}")

    def wait_output(self, needle: bytes, timeout: float = 5) -> None:
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            self.pump(0.05)
            if needle in self.output:
                return
        raise Timeout(f"等待输出 {needle!r} 超时, 已有: {self.output[-2000:]!r}")

    def exec(self, cmd: str, rid: str) -> str:
        self.send({"t": "exec", "id": rid, "cmd": cmd})
        self.wait_status("RUN " + rid)
        return self.wait_status("DONE " + rid + " ")

    def cleanup(self) -> None:
        # 先继续消费待取输出，再关写端，最后有界收尸。
        with suppress(OSError, ValueError):
            self.pump(0.2)
        with suppress(OSError):
            self.proc.stdin.close()
        end = time.monotonic() + 4
        while time.monotonic() < end:
            with suppress(OSError, ValueError):
                self.pump(0.05)
            pid, status = os.waitpid(self.proc.pid, os.WNOHANG)
            if pid == self.proc.pid:
                self.proc.returncode = status
                break
            if time.monotonic() > end - 2:
                try:
                    os.killpg(self.proc.pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    try:
                        os.kill(self.proc.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
        else:
            raise AssertionError("清理超时：子进程未能回收")
        with suppress(OSError):
            self.proc.stdout.close()
            self.proc.stderr.close()


def case(name: str) -> Callable[[Callable[[Component], None]], Callable[[], None]]:
    def deco(fn: Callable[[Component], None]) -> Callable[[], None]:
        def run() -> None:
            c = Component()
            try:
                fn(c)
                print("PASS", name)
            finally:
                c.cleanup()

        run.__name__ = name
        return run

    return deco


@case("timeout_selftest")
def timeout_selftest(c: Component) -> None:
    c.wait_status("READY ")
    c.send({"t": "exec", "id": "t1", "cmd": "sleep 30"})
    c.wait_status("RUN t1")
    raised = False
    try:
        c.wait_status("DONE t1 ", timeout=1.5)
    except Timeout:
        raised = True
    assert raised, "超时分支没有返回"
    # cleanup 必须能在有限时间内杀掉并回收 sleep 与 bash。


@case("state_cd_export_source")
def state(c: Component) -> None:
    c.wait_status("READY ")
    import tempfile

    d = tempfile.mkdtemp(prefix="wt-state-")
    with open(os.path.join(d, "env.sh"), "w") as f:
        f.write("export WT_SOURCED=yes\n")
    assert c.exec("cd " + d, "s1").split()[2] == "0"
    assert c.exec("export WT_TEST=ready", "s2").split()[2] == "0"
    assert c.exec("source ./env.sh", "s3").split()[2] == "0"
    c.exec(
        'printf "PWD=%s TEST=%s SOURCED=%s\\n" "$PWD" "$WT_TEST" "$WT_SOURCED"', "s4"
    )
    c.wait_output((f"PWD={d} TEST=ready SOURCED=yes").encode())
    assert c.exec("(exit 7)", "s5").split()[2] == "7"
    assert c.exec("true", "s6").split()[2] == "0"


@case("prompt_spoofing")
def spoof(c: Component) -> None:
    c.wait_status("READY ")
    c.exec(
        "PS1='DONE fake 0 Zm9v'; printf 'DONE s1 0 Zm9v\\nREADY fake\\n'; (exit 3)",
        "s1",
    )
    line = next(s for s in c.status if s.startswith("DONE s1 "))
    assert line.split()[2] == "3", line
    assert c.exec("true", "s2").split()[2] == "0"
    assert not any("fake" in s for s in c.status), c.status


@case("stdin_foreground")
def stdin_fg(c: Component) -> None:
    import tempfile

    d = tempfile.mkdtemp(prefix="wt-state-")
    c.wait_status("READY ")
    c.exec("cd " + d, "a1")
    c.send(
        {
            "t": "exec",
            "id": "a2",
            "cmd": "python3 -c 'import sys; print(\"GOT:\"+sys.stdin.readline().strip())'",
        }
    )
    c.wait_status("RUN a2")
    time.sleep(0.3)
    # 键盘字节由后端经控制通道转发到 PTY master。
    import base64

    c.send({"t": "keyboard", "data": base64.b64encode(b"hello\r").decode()})
    c.wait_output(b"GOT:hello")
    line = c.wait_status("DONE a2 ")
    assert line.split()[2] == "0"
    c.exec('printf "STILL=%s\\n" "$PWD"', "a3")
    c.wait_output((f"STILL={d}").encode())


@case("ctrl_c_sleep")
def ctrl_c(c: Component) -> None:
    import tempfile

    d = tempfile.mkdtemp(prefix="wt-state-")
    c.wait_status("READY ")
    c.exec("cd " + d, "b1")
    c.send({"t": "exec", "id": "b2", "cmd": "sleep 30"})
    c.wait_status("RUN b2")
    time.sleep(0.3)
    c.send({"t": "keyboard", "data": "Aw=="})
    line = c.wait_status("DONE b2 ")
    assert line.split()[2] == "130", line
    c.exec('printf "ALIVE=%s\\n" "$PWD"', "b3")
    c.wait_output((f"ALIVE={d}").encode())


@case("resize_sigwinch")
def resize(c: Component) -> None:
    c.wait_status("READY ")
    c.send(
        {
            "t": "exec",
            "id": "r1",
            "cmd": 'python3 -c \'import signal,sys,time; signal.signal(signal.SIGWINCH, lambda *a: print("WINCH", flush=True)); print("ARMED", flush=True); time.sleep(3)\'',
        }
    )
    c.wait_status("RUN r1")
    c.wait_output(b"ARMED")
    c.send({"t": "resize", "rows": 40, "cols": 120})
    c.wait_output(b"WINCH")
    line = c.wait_status("DONE r1 ", timeout=6)
    assert line.split()[2] == "0"
    c.exec("stty size", "r2")
    c.wait_output(b"40 120")


@case("subshell_foreground")
def subshell(c: Component) -> None:
    c.wait_status("READY ")
    c.send({"t": "exec", "id": "c1", "cmd": "(sleep 30)"})
    c.wait_status("RUN c1")
    time.sleep(0.3)
    c.send({"t": "keyboard", "data": "Aw=="})
    line = c.wait_status("DONE c1 ")
    assert line.split()[2] == "130", line
    # 交互式子 Shell 占用前台，键盘 exit 退出子 Shell。
    c.send({"t": "exec", "id": "c2", "cmd": "bash --norc -i"})
    c.wait_status("RUN c2")
    time.sleep(0.3)
    c.send(
        {"t": "keyboard", "data": __import__("base64").b64encode(b"exit\r").decode()}
    )
    line = c.wait_status("DONE c2 ", timeout=6)
    assert line.split()[2] == "0", line
    assert c.exec("true", "c3").split()[2] == "0"


@case("bash_self_exit")
def self_exit(c: Component) -> None:
    c.wait_status("READY ")
    c.send({"t": "exec", "id": "d1", "cmd": "exit 5"})
    end = time.monotonic() + 5
    while time.monotonic() < end:
        c.pump(0.05)
        if c.exit_info is not None:
            break
    assert c.exit_info == {"exit": 5}, c.exit_info


@case("human_takeover")
def human(c: Component) -> None:
    import tempfile

    d = tempfile.mkdtemp(prefix="wt-state-")
    c.wait_status("READY ")
    # 人工未提交输入：先键入部分文本，再进入人工模式，回车后应原样执行。
    c.send(
        {"t": "keyboard", "data": __import__("base64").b64encode(b"echo KEEP").decode()}
    )
    time.sleep(0.2)
    c.send({"t": "human"})
    c.wait_status("HPROMPT")
    c.send({"t": "keyboard", "data": "DQ=="})
    c.wait_output(b"KEEP")
    c.wait_status("HDONE 0 ")
    c.send(
        {
            "t": "keyboard",
            "data": __import__("base64")
            .b64encode(("cd " + d + "\r").encode())
            .decode(),
        }
    )
    c.wait_status("HDONE 0 ")
    c.send({"t": "release"})
    c.wait_status("READY ")
    c.exec('printf "HUMANPWD=%s\\n" "$PWD"', "h1")
    c.wait_output((f"HUMANPWD={d}").encode())


@case("human_empty_enter")
def human_empty(c: Component) -> None:
    import base64

    c.wait_status("READY ")
    c.send({"t": "human"})
    c.wait_status("HPROMPT")
    c.send({"t": "keyboard", "data": base64.b64encode(b"\r").decode()})
    # 空行也必须消费完成通知，否则后端 pending 永久挂起。
    c.wait_status("HDONE 0 ", timeout=3)
    c.send({"t": "release"})
    c.wait_status("READY ")
    assert c.exec("true", "e1").split()[2] == "0"


@case("paste_full_write")
def paste_full(c: Component) -> None:
    import base64

    c.wait_status("READY ")
    c.send(
        {
            "t": "exec",
            "id": "p1",
            "cmd": "python3 -c 'import sys,tty; tty.setraw(sys.stdin.fileno()); d=sys.stdin.buffer.read(200000); print(\"LEN\", len(d), flush=True)'",
        }
    )
    c.wait_status("RUN p1")
    time.sleep(0.3)
    data = b"A" * 200000
    c.send({"t": "paste", "id": "k1", "data": base64.b64encode(data).decode()})
    # 远端完整写入（含短写循环）后才回执。
    c.wait_status("POK k1", timeout=15)
    c.wait_output(b"LEN 200000", timeout=10)
    assert c.wait_status("DONE p1 ").split()[2] == "0"


CASES = [
    timeout_selftest,
    state,
    spoof,
    stdin_fg,
    ctrl_c,
    resize,
    subshell,
    self_exit,
    human,
    human_empty,
    paste_full,
]

if __name__ == "__main__":
    only = sys.argv[3:]
    for fn in CASES:
        if only and fn.__name__ not in only:
            continue
        fn()
    print("ALL PASS", MODE)
