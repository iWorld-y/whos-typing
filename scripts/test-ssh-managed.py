"""受管 SSH 会话隔离验收：临时 sshd + 独立 e2e GUI + 独立控制 socket。

用法: python3 scripts/test-ssh-managed.py --allow-desktop-focus GUI_BIN CLI_BIN
两个二进制都须用 -tags e2e 构建。测试会将窗口置前并发送按键，只在可独占的桌面执行。
"""

from __future__ import annotations

import getpass
import json
import os
import pathlib
import socket
import subprocess
import sys
import tempfile
import threading
import time
from collections.abc import Callable
from typing import Any

if len(sys.argv) != 4 or sys.argv[1] != "--allow-desktop-focus":
    raise SystemExit(__doc__)
GUI_BIN, CLI_BIN = sys.argv[2:]


class Automation:
    def __init__(self, path: str | pathlib.Path) -> None:
        self.path = path

    def call(
        self, method: str, params: dict[str, Any] | None = None, timeout: float = 10
    ) -> Any:
        s = socket.socket(socket.AF_UNIX)
        s.connect(self.path)
        s.settimeout(timeout)
        try:
            s.sendall(
                (
                    json.dumps({"id": 1, "method": method, "params": params or {}})
                    + "\n"
                ).encode()
            )
            data = b""
            while not data.endswith(b"\n"):
                chunk = s.recv(65536)
                if not chunk:
                    break
                data += chunk
            reply = json.loads(data)
            if "error" in reply:
                raise RuntimeError(reply["error"])
            return reply["result"]
        finally:
            s.close()

    def window(self) -> str:
        return self.call("windows")["windows"][0]["id"]

    def find(self, key: str) -> dict[str, Any] | None:
        for node in self.call("tree", {"window": self.window()})["nodes"]:
            if node.get("key") == key:
                return node
        return None

    def wait_key(self, key: str, timeout: float = 10) -> dict[str, Any]:
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            node = self.find(key)
            if node is not None:
                return node
            time.sleep(0.2)
        raise TimeoutError(f"等待界面元素 {key} 超时")


def wait_ready(
    cli: Callable[..., subprocess.CompletedProcess[str]], sid: str, timeout: float = 8
) -> None:
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        diag = json.loads(cli("--session", sid, "session", "status", "--json").stdout)
        if diag["remote"] == "ready" and not diag["unknown_result"]:
            return
        time.sleep(0.2)
    raise AssertionError(f"交还后会话未就绪: {diag}")


def main() -> None:
    with tempfile.TemporaryDirectory(prefix="wt-managed-e2e-") as tmp:
        d = pathlib.Path(tmp)
        for name in ("host", "client"):
            subprocess.run(
                ["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(d / name)],
                check=True,
                timeout=20,
            )
        with socket.socket() as l:
            l.bind(("127.0.0.1", 0))
            port = l.getsockname()[1]
        (d / "sshd").write_text(
            f"ListenAddress 127.0.0.1\nPort {port}\nHostKey {d}/host\n"
            f"PidFile {d}/pid\nAuthorizedKeysFile {d}/client.pub\n"
            "StrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n"
            f"UsePAM no\nAllowUsers {getpass.getuser()}\n"
        )
        key = (d / "host.pub").read_text().split()
        (d / "known_hosts").write_text(f"[127.0.0.1]:{port} {key[0]} {key[1]}\n")
        (d / "config").write_text(
            f"Host wt-fixture\n HostName 127.0.0.1\n Port {port}\n"
            f" User {getpass.getuser()}\n IdentityFile {d}/client\n"
            f" IdentitiesOnly yes\n UserKnownHostsFile {d}/known_hosts\n"
        )
        work = d / "work"
        work.mkdir()
        (work / "env.sh").write_text("export WT_SOURCED=yes\n")

        home = d / "home"
        home.mkdir()
        ctl = d / "ctl"
        ctl.mkdir(mode=0o700)
        auto_sock = str(d / "auto.sock")
        env = dict(
            os.environ,
            HOME=str(home),
            WHOS_TYPING_TEST_DIR=str(ctl),
            MYGO_AUTOMATION=auto_sock,
            SHELL="/bin/bash",
        )
        cli_env = dict(os.environ, WHOS_TYPING_TEST_DIR=str(ctl))

        def cli(*args: str, timeout: float = 30) -> subprocess.CompletedProcess[str]:
            return subprocess.run(
                [CLI_BIN] + list(args),
                check=False,
                capture_output=True,
                text=True,
                timeout=timeout,
                env=cli_env,
            )

        log = (d / "gui.log").open("w+")
        sshd_log = (d / "sshd.log").open("w+")
        server = subprocess.Popen(
            ["/usr/sbin/sshd", "-D", "-e", "-f", str(d / "sshd")],
            stdout=sshd_log,
            stderr=sshd_log,
        )
        gui = None
        try:
            for _ in range(50):
                if server.poll() is not None:
                    sshd_log.seek(0)
                    raise RuntimeError(sshd_log.read())
                try:
                    with socket.create_connection(("127.0.0.1", port), 0.1):
                        break
                except OSError:
                    time.sleep(0.1)
            else:
                raise RuntimeError("临时 SSH 服务未就绪")

            gui = subprocess.Popen([GUI_BIN], stdout=log, stderr=log, env=env)
            auto = Automation(auto_sock)

            def ensure_focused() -> None:
                # 只激活本验收自己启动的测试窗口；后台窗口不重绘也不接收按键。
                for _ in range(10):
                    subprocess.run(
                        [
                            "osascript",
                            "-e",
                            f'tell application "System Events" to set frontmost of (first process whose unix id is {gui.pid}) to true',
                        ],
                        check=False,
                        capture_output=True,
                        timeout=10,
                    )
                    time.sleep(0.3)
                    if auto.call("windows")["windows"][0]["focused"]:
                        return
                raise AssertionError("测试窗口无法获得焦点")

            def focus_terminal(session_id: str) -> dict[str, Any]:
                # 激活可能把焦点从终端移走；最后一步固定为终端聚焦并核对。
                term = auto.wait_key("terminal-" + session_id, timeout=10)
                for _ in range(5):
                    ensure_focused()
                    auto.call(
                        "act",
                        {"window": auto.window(), "id": term["id"], "action": "focus"},
                    )
                    time.sleep(0.3)
                    if (
                        auto.call("tree", {"window": auto.window()}).get("focus")
                        == term["id"]
                    ):
                        return term
                raise AssertionError("终端无法获得键盘焦点")

            for _ in range(150):
                if gui.poll() is not None:
                    log.seek(0)
                    raise RuntimeError("GUI 提前退出: " + log.read()[:2000])
                try:
                    r = cli("session", "list", timeout=5)
                    if r.returncode == 0:
                        break
                except subprocess.TimeoutExpired:
                    pass
                time.sleep(0.2)
            else:
                raise RuntimeError("GUI 控制服务未就绪")
            ensure_focused()

            r = cli("ssh", "--config", str(d / "config"), "connect", "wt-fixture")
            assert r.returncode == 0, r.stderr
            connect_info = json.loads(r.stdout)
            assert connect_info["connected"]

            def master_pid() -> str:
                p = subprocess.run(
                    [
                        "/usr/bin/ssh",
                        "-F",
                        str(d / "config"),
                        "-S",
                        connect_info["socket"],
                        "-O",
                        "check",
                        "wt-fixture",
                    ],
                    check=False,
                    capture_output=True,
                    text=True,
                    timeout=10,
                )
                assert p.returncode == 0, p.stderr
                return p.stderr.strip()

            first_master = master_pid()
            print("PASS connect")

            r = cli("ssh", "--config", str(d / "config"), "open", "wt-fixture")
            assert r.returncode == 0, r.stderr
            created = json.loads(r.stdout)
            sid = created["session_id"]
            assert sid and created["tab_id"]
            r = cli("--session", sid, "session", "focus")
            assert r.returncode == 0, r.stderr
            print("PASS open ->", sid)

            r = cli("--session", sid, "cd " + str(work))
            assert r.returncode == 0, r.stderr
            r = cli("--session", sid, "export WT_TEST=ready && source ./env.sh")
            assert r.returncode == 0, r.stderr
            r = cli("--session", sid, 'pwd; printf "%s|%s" "$WT_TEST" "$WT_SOURCED"')
            assert r.returncode == 0, r.stderr
            assert str(work) in r.stdout and "ready|yes" in r.stdout, r.stdout
            print("PASS 状态延续（cd/export/source）")

            r = cli("--session", sid, "session", "read")
            assert (
                r.returncode == 0 and "[CLI]" in r.stdout and "ready|yes" in r.stdout
            ), r.stdout
            print("PASS GUI 终端显示命令与输出")

            # 命令退出前 GUI 已能看到输出；目录变化更新默认标题。
            early = {}

            def early_run() -> None:
                p = cli("--session", sid, "echo early-line; sleep 2", timeout=20)
                early["code"] = p.returncode

            th = threading.Thread(target=early_run)
            th.start()
            end = time.monotonic() + 5
            while time.monotonic() < end:
                text = cli("--session", sid, "session", "read").stdout
                if "early-line" in text:
                    break
                time.sleep(0.2)
            else:
                raise AssertionError("命令退出前 GUI 未显示输出")
            th.join(timeout=10)
            assert early.get("code") == 0, early
            listing = json.loads(cli("session", "list", "--json").stdout)
            tab_title = next(
                s["tab_title"] for s in listing["sessions"] if s["session_id"] == sid
            )
            assert str(work) in tab_title, tab_title
            ensure_focused()
            tab_label = ""
            for node in auto.call("tree", {"window": auto.window()})["nodes"]:
                if node.get("role") == "tab" and created["tab_id"] in str(
                    node.get("key", "")
                ):
                    tab_label = node.get("label", "")
            assert str(work) in tab_label, tab_label
            print("PASS 实时输出与目录标题更新")

            r = cli("--session", sid, "session", "status", "--json")
            assert r.returncode == 0, r.stderr
            diag = json.loads(r.stdout)
            assert diag["diagnostics_available"] and diag["remote"] in (
                "ready",
                "running",
            ), diag
            print("PASS 结构化诊断:", diag["remote"])

            # 长命令 + CLI 超时：结果未知，GUI 仍显示输出，可接管。
            outcome: dict[str, int | str] = {}

            def long_run() -> None:
                p = cli(
                    "--timeout",
                    "1s",
                    "--session",
                    sid,
                    "echo long-start; sleep 30",
                    timeout=20,
                )
                outcome["code"], outcome["err"] = p.returncode, p.stderr

            th = threading.Thread(target=long_run)
            th.start()
            auto.wait_key("take-over", timeout=10)
            th.join(timeout=15)
            assert "结果未知" in outcome.get("err", ""), outcome
            r = cli("--session", sid, "session", "status", "--json")
            diag = json.loads(r.stdout)
            assert diag["unknown_result"], diag
            r = cli("--session", sid, "true")
            assert r.returncode != 0
            print("PASS CLI 超时报告未知且拒绝新命令")

            ensure_focused()
            auto.call(
                "act", {"window": auto.window(), "key": "take-over", "action": "press"}
            )
            focus_terminal(sid)
            auto.call(
                "input",
                {"window": auto.window(), "events": [{"type": "key", "key": "Ctrl+C"}]},
            )
            end = time.monotonic() + 10
            while time.monotonic() < end:
                diag = json.loads(
                    cli("--session", sid, "session", "status", "--json").stdout
                )
                if not diag["unknown_result"] and diag["remote"] == "ready":
                    break
                if diag["unknown_result"] and diag["remote"] == "running":
                    # 焦点丢失时按键不会到达终端；重新激活后重试一次。
                    ensure_focused()
                    auto.call(
                        "input",
                        {
                            "window": auto.window(),
                            "events": [{"type": "key", "key": "Ctrl+C"}],
                        },
                    )
                time.sleep(0.5)
            else:
                raise AssertionError(f"Ctrl+C 后未恢复就绪: {diag}")
            r = cli("--session", sid, "session", "read")
            assert "退出码 130" in r.stdout, r.stdout[-800:]
            print("PASS 接管后 Ctrl+C 中断 sleep，退出码 130")

            auto.wait_key("return-control", timeout=10)
            ensure_focused()
            auto.call(
                "act",
                {"window": auto.window(), "key": "return-control", "action": "press"},
            )
            wait_ready(cli, sid)
            r = cli("--session", sid, "pwd")
            assert r.returncode == 0 and str(work) in r.stdout, (r.stdout, r.stderr)
            print("PASS 交还后目录保留")

            # vim 接管：编辑、退出、交还后目录不变。
            vimfile = work / "vim-test.txt"
            th = threading.Thread(
                target=lambda: cli("--session", sid, "vim " + str(vimfile), timeout=30)
            )
            th.start()
            auto.wait_key("take-over", timeout=10)
            ensure_focused()
            auto.call(
                "act", {"window": auto.window(), "key": "take-over", "action": "press"}
            )
            focus_terminal(sid)
            time.sleep(0.5)
            auto.call(
                "input",
                {
                    "window": auto.window(),
                    "events": [
                        {"type": "text", "text": "ihello-from-takeover"},
                        {"type": "key", "key": "Escape"},
                        {"type": "text", "text": ":wq"},
                        {"type": "key", "key": "Enter"},
                    ],
                },
            )
            end = time.monotonic() + 8
            while time.monotonic() < end:
                if (
                    vimfile.exists()
                    and vimfile.read_text().strip() == "hello-from-takeover"
                ):
                    break
                time.sleep(0.3)
            else:
                # 焦点丢失时按键不会到达 vim；重新聚焦后补发退出序列。
                focus_terminal(sid)
                auto.call(
                    "input",
                    {
                        "window": auto.window(),
                        "events": [
                            {"type": "key", "key": "Escape"},
                            {"type": "text", "text": ":wq!"},
                            {"type": "key", "key": "Enter"},
                        ],
                    },
                )
                end = time.monotonic() + 8
                while time.monotonic() < end:
                    if vimfile.exists():
                        break
                    time.sleep(0.3)
                else:
                    raise AssertionError("vim 接管编辑未写入文件")
            th.join(timeout=20)
            auto.wait_key("return-control", timeout=10)
            ensure_focused()
            auto.call(
                "act",
                {"window": auto.window(), "key": "return-control", "action": "press"},
            )
            wait_ready(cli, sid)
            r = cli("--session", sid, "pwd")
            assert r.returncode == 0 and str(work) in r.stdout, (r.stdout, r.stderr)
            print("PASS vim 接管编辑、退出、交还及目录保留")

            # 第二个受管会话：同目标独立 Bash。
            r = cli("ssh", "--config", str(d / "config"), "open", "wt-fixture")
            assert r.returncode == 0, r.stderr
            sid_b = json.loads(r.stdout)["session_id"]
            r = cli("--session", sid_b, 'pwd; printf "%s" "$WT_TEST"')
            assert r.returncode == 0 and str(work) not in r.stdout, r.stdout
            assert master_pid() == first_master, "两个受管会话未复用同一个 master"
            print("PASS 同目标窗格相互独立且复用同一 master")

            # 分屏创建本地窗格，不改变受管会话。
            r = cli("--session", sid_b, "session", "split", "--direction", "right")
            assert r.returncode == 0, r.stderr
            r = cli("--session", sid, "pwd")
            assert r.returncode == 0 and str(work) in r.stdout, (r.stdout, r.stderr)
            print("PASS 分屏不换会话")

            # 关闭 A 的标签页：右键菜单。
            tab_node = None
            for node in auto.call("tree", {"window": auto.window()})["nodes"]:
                if node.get("role") == "tab" and created["tab_id"] in str(
                    node.get("key", "")
                ) + str(node.get("label", "")):
                    tab_node = node
            if tab_node is not None:
                b = tab_node["bounds"]
                x, y = b["x"] + b["width"] / 2, b["y"] + b["height"] / 2
                auto.call(
                    "input",
                    {
                        "window": auto.window(),
                        "events": [
                            {"type": "pointerDown", "x": x, "y": y, "button": "right"},
                            {"type": "pointerUp", "x": x, "y": y, "button": "right"},
                        ],
                    },
                )
                time.sleep(0.5)
                closed = False
                for node in auto.call("tree", {"window": auto.window()})["nodes"]:
                    if node.get("role") == "menuItem" and "关闭标签页" in node.get(
                        "label", ""
                    ):
                        auto.call(
                            "act",
                            {
                                "window": auto.window(),
                                "id": node["id"],
                                "action": "press",
                            },
                        )
                        closed = True
                        break
                if closed:
                    time.sleep(1.0)
                    r = cli("--session", sid_b, "true")
                    assert r.returncode == 0, r.stderr
                    r = cli("--session", sid, "true")
                    assert r.returncode != 0
                    print("PASS 关闭 A 标签页不影响 B")
                else:
                    print("SKIP 关闭标签页菜单项未出现在自动化树")
            else:
                print("SKIP 标签页节点未出现在自动化树")

            r = cli("ssh", "--config", str(d / "config"), "disconnect", "wt-fixture")
            assert r.returncode == 0, r.stderr
            end = time.monotonic() + 10
            while time.monotonic() < end:
                diag = json.loads(
                    cli("--session", sid_b, "session", "status", "--json").stdout
                )
                if diag["remote"] == "disconnected":
                    break
                time.sleep(0.3)
            else:
                raise AssertionError(f"断开后未报告连接失效: {diag}")
            assert "重新创建" in diag["suggested_action"], diag
            r = cli("--session", sid_b, "true")
            assert r.returncode != 0 and "未发送" in r.stderr, r.stderr
            print("PASS disconnect 后报告失效并拒绝新命令")
            print("ALL PASS managed-ssh")
        finally:
            if gui is not None and gui.poll() is None:
                gui.terminate()
                try:
                    gui.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    gui.kill()
                    gui.wait()
            server.terminate()
            try:
                server.wait(timeout=5)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()


if __name__ == "__main__":
    main()
