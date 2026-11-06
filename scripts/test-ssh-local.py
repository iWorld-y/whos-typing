#!/usr/bin/env python3
"""可选真实 SSH 验收：临时密钥、127.0.0.1 高端口，不接触生产服务器。"""
import getpass
import json
import pathlib
import socket
import subprocess
import tempfile
import time


def main():
    cli = pathlib.Path('output/whos-typing-cli').resolve()
    with tempfile.TemporaryDirectory(prefix='wt-ssh-e2e-') as tmp:
        directory = pathlib.Path(tmp)
        for name in ('host', 'client'):
            subprocess.run(
                ['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(directory / name)],
                check=True,
            )
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            port = listener.getsockname()[1]
        (directory / 'sshd').write_text(
            f'ListenAddress 127.0.0.1\nPort {port}\nHostKey {directory}/host\n'
            f'PidFile {directory}/pid\nAuthorizedKeysFile {directory}/client.pub\n'
            'StrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\n'
            f'UsePAM no\nAllowUsers {getpass.getuser()}\n'
        )
        key = (directory / 'host.pub').read_text().split()
        (directory / 'known_hosts').write_text(f'[127.0.0.1]:{port} {key[0]} {key[1]}\n')
        (directory / 'config').write_text(
            f'Host wt-fixture\n HostName 127.0.0.1\n Port {port}\n'
            f' User {getpass.getuser()}\n IdentityFile {directory}/client\n'
            f' IdentitiesOnly yes\n UserKnownHostsFile {directory}/known_hosts\n'
        )
        base = [str(cli), 'ssh', '--config', str(directory / 'config')]

        def call(*args):
            return subprocess.run(base + list(args), capture_output=True, text=True, timeout=20)

        with (directory / 'log').open('w+') as log:
            server = subprocess.Popen(
                ['/usr/sbin/sshd', '-D', '-e', '-f', str(directory / 'sshd')],
                stdout=log, stderr=log,
            )
            try:
                for _ in range(30):
                    if server.poll() is not None:
                        log.seek(0)
                        raise RuntimeError(log.read())
                    try:
                        with socket.create_connection(('127.0.0.1', port), 0.1):
                            break
                    except OSError:
                        time.sleep(0.1)
                else:
                    raise RuntimeError('临时 SSH 服务未就绪')

                result = call('connect', 'wt-fixture')
                assert result.returncode == 0, result.stderr
                first = json.loads(result.stdout)
                assert first['connected']

                def master_status():
                    return subprocess.run(
                        ['/usr/bin/ssh', '-F', str(directory / 'config'), '-S', first['socket'],
                         '-O', 'check', 'wt-fixture'],
                        capture_output=True, text=True, check=True, timeout=10,
                    ).stderr

                first_master = master_status()
                result = call('connect', 'wt-fixture')
                assert result.returncode == 0, result.stderr
                assert json.loads(result.stdout)['socket'] == first['socket']
                assert master_status() == first_master, '重复 connect 更换了 master'
                result = call('run', 'wt-fixture', "printf 'remote-ok'; printf 'remote-err' >&2; exit 7")
                assert (result.returncode, result.stdout, result.stderr) == (7, 'remote-ok', 'remote-err'), result
                result = call('disconnect', 'wt-fixture')
                assert result.returncode == 0, result.stderr
                assert not json.loads(result.stdout)['connected']
                result = call('run', 'wt-fixture', 'echo must-not-run')
                assert result.returncode != 0 and 'must-not-run' not in result.stdout
                print('PASS: connect, same master, stdout/stderr/exit, disconnect, fail closed')
            finally:
                try:
                    call('disconnect', 'wt-fixture')
                finally:
                    server.terminate()
                    try:
                        server.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        server.kill()
                        server.wait()


if __name__ == '__main__':
    main()
