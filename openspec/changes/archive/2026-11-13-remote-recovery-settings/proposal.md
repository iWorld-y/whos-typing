# Proposal

## Why

[Issue #4](https://github.com/iWorld-y/whos-typing/issues/4) 暴露了远端内层 Bash 退出后的恢复缺口：等待中的 CLI 收不到结束标记，未知请求又阻止重新接入，用户只能新开会话。首次远端接入目前必须逐次确认，用户希望通过设置选择自动允许。

## What Changes

- 为已接入 Bash 的退出增加可验证的通知，并区分命令结果与接入是否有效；失效时不再让 CLI 只能等待超时。
- 提供用户主动发起的失效绑定恢复，保留旧请求结果未知的事实，通过新握手恢复同一终端，无需关闭 SSH 或重建窗格。
- 新增设置窗口及本机持久化的“自动允许 Agent 接入远端 Shell”开关；开启后首次自动接入不弹确认框，关闭后保留确认流程。
- 保留人类接管、未提交输入、并发写入、会话绑定和超时不自动重试等保护；自动允许不等于自动确认旧请求结果。
- 补齐可操作的 CLI 状态信息、错误提示、回归测试及使用说明。

## Capabilities

### New Capabilities

- `application-settings`: 设置窗口、自动接入选项的保存及立即生效行为。

### Modified Capabilities

- `agent-terminal-control`: 自动接入审批策略、远端失效通知、未知请求恢复和控制状态诊断。

## Impact

主要涉及 `internal/shell/remote.go`、`remote_recovery.go`、`input.go`、`ownership.go`，以及 `internal/app` 中的接入确认、控制栏和设置入口。CLI 状态扩展涉及 `internal/control` 与 `cmd/whos-typing-cli`，必须保留已有调用兼容性。

需要更新 `docs/development.md` 和仓库内 CLI 技能说明。保持上游 MyGo 依赖，不升级依赖，不修改服务器或安装版 App。本变更不处理镜像下载网络问题，也不改变共享 Shell 中 `cd`、`export` 和 Shell 选项的延续语义。
