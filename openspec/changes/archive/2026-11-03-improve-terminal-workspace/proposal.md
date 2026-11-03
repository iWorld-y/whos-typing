# Proposal

## Why

当前终端缺少窗格内搜索，独立标签栏占用垂直空间，CLI 返回的长 ID 也增加了 Agent 选择会话的成本。将搜索、标题栏标签和短标识一起完善，可以保持人工快捷操作与 Agent 精确寻址的一致性，对应 GitHub issue #1。

## What Changes

- 增加当前窗格的屏幕及保留滚动历史搜索，提供搜索栏、结果计数、高亮、前后跳转和焦点恢复；搜索不发送 Shell 输入，不改变控制权。
- 将标题和标签放到 macOS 红绿灯右侧同一行。只有一个标签时只显示标题；多个标签均分扣除窗口按钮与间距后的可用宽度。移除旧标签行及左右按钮。
- 默认标题为 `username@hostname:/path`，采用标签内最后选中窗格的信息；保留显式重命名。页面不显示 ID 或位置编号。
- 增加 `Cmd+1` 至 `Cmd+9`，按当前从左到右的位置切换，新标签追加到末尾。不存在的位置不执行操作。
- **BREAKING**：新建会话使用 `session_01`、`session_02` 等 ID，标签使用 `tab_01`、`tab_02` 等 ID。两组十进制序号独立递增，数字部分至少两位，超过 99 后自然增长；关闭或重启后不复用。CLI 文本和 JSON 直接返回完整 `tab_id`、`session_id`，不接受位置或不完整前缀代替精确 ID。CLI 参数和 JSON 字段名不变。
- **BREAKING**：取消递增数字默认标签名。默认标题随会话元信息更新，手动重命名优先且不受 Shell 标题覆盖。

## Capabilities

### New Capabilities

- `terminal-search`：当前窗格的只读文本搜索、导航、显示和搜索生命周期。

### Modified Capabilities

- `terminal-workspace`：标题栏布局、动态默认标题、带类型前缀的稳定标识、按位置切换标签。
- `session-routing`：短标识发现和精确寻址，以及默认动态标题在 CLI 中的一致性。

## Impact

- 应用层涉及 `internal/app/app.go`、`workspace.go`、`workspace_view.go`、`pane.go` 和相关测试；搜索状态及标题生成应放在专门模块，保持界面编排简洁。
- Shell 层需要在既有本地初始化和远端 Bash 接入协议中提供用户名、主机名和目录元信息，保留提示符状态、退出码和控制权规则。
- 控制层涉及标识分配、会话发现和版本兼容测试；更新 `docs/development.md`、CLI 帮助和仓库维护的 CLI Skill。
- 优先使用原始上游 MyGo 的搜索与自定义标题栏接口。当前固定版本为 `v0.7.4`，已在本地上游提交 `48da1ba` 确认接口；实现前需要验证目标正式版本及配套原生库，不引入 fork、replace 或 go.work 替换。
- 本次不包含跨窗格搜索、正则、历史持久化、拖动排序、远端自动探测或实现代码。当前主工作区的远端恢复改动不纳入本 worktree。
