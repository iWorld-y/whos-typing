# Proposal

## Why

现有窗格的 CLI 执行依赖 Bash 提示符和 PTY 命令注入。当前台是 `vim`、`less` 或未接入的子 Shell 时，CLI 无法通过原路径可靠地提交命令。独立的 `ssh run` 已能复用连接，但各次调用使用不同的 Shell，无法自然延续工作目录和环境。

## What Changes

- 在工作区提供显式创建的受管 SSH 会话。每个会话使用独立且持续运行的远端 Bash，后续 CLI 调用沿用这个 Bash 的工作目录和环境。
- 由执行后端接收 CLI 命令，通过独立于终端输入的控制通道提交。GUI 显示命令文本、实时终端输出和执行结果。
- 用户可以接管同一个前台程序，使用键盘或 `Ctrl+C` 操作。前台程序退出且会话就绪后，用户可以交还 Agent 并继续在原 Bash 执行命令。
- 保留独立 `ssh run`、本地 Shell 窗格和既有远端 Bash 接入。首版支持应用打开期间的会话，不包含应用退出后重连、远端后台任务管理或接管外部建立的 SSH 会话。

## Capabilities

### New Capabilities

- `managed-ssh-session`：创建并管理带 Shell 状态的 SSH 会话，支持命令执行、实时显示、前台输入和会话结束。

### Modified Capabilities

- `agent-terminal-control`：将受管 SSH 会话的执行准入、交还和诊断建立在执行后端的状态上，保留现有控制权和人工输入保护。
- `terminal-workspace`：明确普通新建标签仍启动本地 Shell，并支持显式新建受管 SSH 标签页。

## Impact

涉及 `internal/sshlink` 的连接复用、`internal/control` 的能力协商和会话路由、`internal/app` 的终端接入，以及随会话启动的远端执行组件。首版使用现有 OpenSSH 配置和远端 Bash，验收使用隔离的 SSH 服务和 macOS GUI。
