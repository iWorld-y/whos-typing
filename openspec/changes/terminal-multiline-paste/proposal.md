# Proposal

## Why

Who’s Typing CLI 只能向已就绪的 Shell 提交命令。Issue #5 已用隔离的 MySQL 客户端复现，同样的限制也会影响需要多行输入的 REPL 和终端编辑器。

## What Changes

- 新增通用的 CLI 终端粘贴操作，显式指定窗格，支持保留缩进、空行和换行的文本。
- 根据目标终端当前的括号粘贴模式和输入状态决定是否需要用户确认。多行文本在模式关闭或未知时，必须在目标窗格确认。
- 粘贴不追加 Enter。CLI 报告输入发送状态，结果仍可通过现有的终端快照查看。MySQL 专用结果解析和远程按键 API 不在范围内。

## Capabilities

### New Capabilities

- `terminal-paste`：向指定窗格的前台程序粘贴文本，判断确认条件并报告发送状态。

### Modified Capabilities

## Impact

涉及 CLI 参数与能力协商、会话路由、窗格确认界面、终端输入队列和状态跟踪。现有 Shell 执行契约保持适用。使用 MySQL 和支持括号粘贴的终端夹具验收。
