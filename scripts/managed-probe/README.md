# 受管 Bash 最小验证探针

任务 1.1/1.2 的可重复证据：真实 PTY + 独立命令/状态 FD 的受管 Bash，
先本地验证，再经隔离 sshd 验证。组件与产品实现共用
`internal/shell/managed_component.py`。

```sh
# 本地 PTY（每个用例独立进程，外层 25 秒硬超时）
python3 scripts/managed-probe/runner.py internal/shell/managed_component.py local
# 隔离 SSH（临时密钥 + 127.0.0.1 高端口 sshd，需要 /usr/sbin/sshd 和 ssh-keygen）
python3 scripts/managed-probe/ssh_fixture.py internal/shell/managed_component.py
```

用例：超时分支自检、cd/export/source 状态延续、提示符与伪造通知、
前台 stdin 程序、Ctrl+C 保留 Bash 与目录、SIGWINCH、子 Shell 前台、
Bash 自身退出、人工接管与交还（含未提交输入保留）。
所有等待使用单调时钟截止时间；清理时继续消费 PTY 输出、关闭写端，
再用 waitpid(WNOHANG) 收尸，超时只终止本用例启动的进程组。
