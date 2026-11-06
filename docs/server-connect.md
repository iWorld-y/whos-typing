# 无需 GUI 的服务器连接

目标：Agent 使用已有 OpenSSH 配置连接服务器，后续命令复用同一条连接。无需启动 GUI，也无需向交互提示符粘贴接入代码。原 GUI 仍负责人与 Agent 共用 Shell 的场景。

```sh
./output/whos-typing-cli ssh connect my-server
./output/whos-typing-cli ssh status my-server
./output/whos-typing-cli ssh run my-server 'hostname; id -un; pwd'
./output/whos-typing-cli ssh run my-server 'cd /opt/my-project && git status --short'
./output/whos-typing-cli ssh disconnect my-server
```

`my-server` 是已有 SSH 配置中的别名，也可使用 `user@host`。密钥、端口和跳板机沿用 OpenSSH 配置；本工具不收集密码或修改 SSH 配置。首次主机信任或交互认证需先在普通终端完成；批处理连接遇到这些情况立即失败，不关闭主机密钥验证。

`connect` 显式创建受控 ControlMaster，空闲保留 10 分钟，每 15 秒探测连接，连续 3 次无响应后退出。`status` 输出 JSON；connected 仅表示本地 master 存活，不保证远端服务健康或业务任务成功。远端身份与业务状态须通过实际只读命令确认。

每次 run 使用独立远端命令通道：**不会继承上一次 run 的 cd 或环境变量**。需要的目录和环境在同一命令里明确设置。需要持续交互的 Shell 请使用原 GUI。

`run` 必须先有可用连接，且禁用连接消失后的直连回退。不自动 connect、不自动重试。SSH 返回 255 时报告结果未知（远端命令本身也可能返回 255）；核对远端日志或回执后再决定下一步。输出直接转发，保留 stdout/stderr 和正常退出码。它不提供长任务持久化或任务历史，长任务继续使用服务器上的既有任务管理机制。

`disconnect` 会结束该目标的专属 master，可能中断正在执行的通道，应在工作结束后调用。socket 与锁文件位于当前用户独占的 /tmp/whos-typing-ssh-UID，不操作其他 SSH master。

## 验收

单元测试覆盖恶意目标参数、断线拒绝、连接在检查后消失时不回退、命令字符串不被本机展开、远端退出码和重复连接复用。可运行 `go test ./internal/sshlink ./cmd/whos-typing-cli` 验证。执行 `make build` 后，可运行 `python3 scripts/test-ssh-local.py` 启动临时本机 SSH 服务，验证连接复用、远端输出/退出码及断开后的拒绝执行；需要系统提供 `/usr/sbin/sshd` 和 `ssh-keygen`，不接触已有 SSH 会话。

没有全局 SSH 配置时，可以使用单独的配置文件，无需修改 ~/.ssh/config：

```sh
./output/whos-typing-cli ssh --config /absolute/path/servers.conf connect my-server
./output/whos-typing-cli ssh --config /absolute/path/servers.conf run my-server 'hostname'
```

配置文件示例（将值替换为真实连接信息）：

```sshconfig
Host my-server
    HostName server.example.com
    User my-user
    Port 22
    IdentityFile /absolute/path/to/existing-private-key
    IdentitiesOnly yes
```

显式配置文件的路径和内容参与连接标识；文件修改后必须重新 connect。配置 Include 指向的文件、默认 ~/.ssh/config 或密钥变化时，先用旧配置 disconnect，再修改并 connect。状态仅证明对应 socket 存活，不解析 SSH 别名以猜测远端身份。
