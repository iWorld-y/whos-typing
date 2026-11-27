"""隔离 SSH 验收夹具：临时密钥 + 127.0.0.1 高端口 sshd，运行受管组件探针。"""

import getpass
import json
import os
import pathlib
import socket
import subprocess
import sys
import tempfile
import time

HERE = pathlib.Path(__file__).parent
COMPONENT = sys.argv[1]

with tempfile.TemporaryDirectory(prefix="wt-managed-ssh-") as tmp:
    d = pathlib.Path(tmp)
    for name in ("host", "client"):
        subprocess.run(
            ["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(d / name)],
            check=True,
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
    ssh_args = [
        "/usr/bin/ssh",
        "-F",
        str(d / "config"),
        "-o",
        "BatchMode=yes",
        "-o",
        "StrictHostKeyChecking=yes",
        "-o",
        "ConnectTimeout=10",
        "-T",
        "wt-fixture",
    ]
    env = dict(os.environ, WT_SSH_ARGS=json.dumps(ssh_args))
    with (d / "log").open("w+") as log:
        server = subprocess.Popen(
            ["/usr/sbin/sshd", "-D", "-e", "-f", str(d / "sshd")],
            stdout=log,
            stderr=log,
        )
        try:
            for _ in range(50):
                if server.poll() is not None:
                    log.seek(0)
                    raise RuntimeError(log.read())
                try:
                    with socket.create_connection(("127.0.0.1", port), 0.1):
                        break
                except OSError:
                    time.sleep(0.1)
            else:
                raise RuntimeError("临时 SSH 服务未就绪")
            r = subprocess.run(
                [sys.executable, str(HERE / "runner.py"), COMPONENT, "ssh"],
                check=False,
                timeout=285,
                env=env,
            )
            sys.exit(r.returncode)
        finally:
            server.terminate()
            try:
                server.wait(timeout=5)
            except subprocess.TimeoutExpired:
                server.kill()
                server.wait()
