# Proposal

## Why

whos-typing 尚无应用代码，需要先验证 Go 和 MyGo 能提供可交互的原生终端。首个 MVP 让用户打开窗口并执行本地 Shell 命令，为后续 Agent 接入提供实际可用的客户端基础。

## What Changes

- 初始化 Go 应用，使用 MyGo 原生 UI 与终端插件打开单个终端窗口。
- 启动本地用户 Shell，支持键盘输入、回车执行、输出显示及连续命令操作。
- 处理窗口关闭、Shell 退出和启动失败，释放本次会话资源。
- 提供本地启动说明与验收步骤；本轮以 macOS 为首个验证平台。
- 本版不包含 SSH、CLI + Skill、Agent 接入、后台常驻、多会话、会话恢复、发布安装包或自动更新。

## Capabilities

### New Capabilities

- `local-terminal`: 原生终端窗口中的本地 Shell 输入、执行、输出及会话生命周期。

### Modified Capabilities

无。

## Impact

- 新增 Go 模块、应用入口和最小客户端模块，当前没有既有代码或接口需要迁移。
- 依赖 `github.com/egoist/mygo`、`ui` 与 `plugins/terminal`，终端插件运行时需要 libghostty-vt。
- 依赖必须来自原始提供方 `https://github.com/egoist/mygo`，固定上游版本或提交；不使用 fork 或本地路径替换。本地框架目录仅作为本次规划参考。
- 新增 `docs/` 下的开发与验收说明；不修改 MyGo 框架。
