# Proposal

## Why

当前控制提示位于窗口底部并占据一整条区域，与期望的顶部悬浮胶囊样式不一致。用户希望调整提示的外观和位置，同时保留现有的接管与交还交互。

## What Changes

- 将控制状态和操作按钮组合为终端内容上方、水平居中的悬浮胶囊，使用较通透的毛玻璃背景，减少视觉干扰，并检查文字的视觉垂直居中。
- Agent 控制时显示「agent 正在控制」和「接管」；人类主动接管后，在同一位置显示「人类正在控制」和「交还 Agent」。保留现有的交还失败提示。
- 移除底部整行控制栏，不为顶部胶囊预留终端空间；允许胶囊局部遮挡终端文字。
- 保持控制权切换、命令执行、只读观察、60 秒自动交还和粒子效果的现有行为。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `agent-terminal-control`：将底部控制栏调整为顶部居中的悬浮胶囊，并明确可见状态与覆盖范围。

## Impact

- 界面布局及其测试：`internal/app/control_view.go`、`internal/app/control_view_test.go`。
- 使用说明：`docs/development.md` 中涉及控制提示位置的描述。
- 不修改 Shell 控制权协议、CLI 参数或上游 MyGo 依赖。
