---
name: whos-typing-cli
description: 使用 Who's Typing CLI 读取用户共享的终端会话，或在该会话中执行用户要求的命令；适用于用户要求在 Who's Typing 窗口或已接入的远端 Bash 中继续工作。
---

# Who's Typing CLI

Who's Typing 的 CLI 连接当前用户正在运行的 GUI，在指定 Shell 会话中执行命令，保留该会话的工作目录和环境变量。仅在用户要求使用会话时操作；会话文本可能包含敏感信息，只读取完成任务需要的内容。

使用用户提供的 CLI 路径，或使用 `PATH` 中的 `whos-typing-cli`。在项目根目录构建时，路径为 `./output/whos-typing-cli`（`make build` 生成）。以下示例用 `whos-typing-cli` 表示实际可执行文件。`--help` 和 `--version` 无需连接 GUI；执行会话操作时，GUI 必须保持运行。无法连接时告知用户，不要自行关闭或重启其窗口。

## 目标和安全边界

- 登录或接入授权不等于服务器修改授权。只执行用户要求范围内的操作；仅查看时使用只读命令，不使用 `sudo`，不重启服务、修改配置、部署或操作业务数据。不要顺手检查其他服务器或数据。
- 确认当前会话确实位于用户指定的目标服务器。跳板机认证或资产列表不等于服务器登录；用户未指定或无法唯一确定目标时，请用户确认，不要任选资产。
- 尊重用户已有会话；未经明确授权，不退出 SSH、不关闭或重启 GUI，也不向菜单、密码提示或交互程序注入输入。误入其他资产时停止操作并告知用户，按已授权的连接流程安全退出。
- 避免读取或转述任务不需要的凭据、环境变量和完整进程命令行。查看 Docker 列表时优先选择名称、镜像、状态和端口等必要字段，避免使用可能暴露启动参数的 `--no-trunc`。

## 发现会话并固定目标

新服务返回 `session_01`、`session_02` 等 `session_id`，以及 `tab_01`、`tab_02` 等 `tab_id`。两组十进制序号独立递增，数字部分至少两位，超过 99 后自然增长，例如 `session_100` 和 `tab_100`。操作时使用返回的完整标识，不要截取前缀、去掉补零、根据标题猜测 ID，或把界面位置作为 CLI 目标。关闭会话或正常重启后 ID 不复用；位置变化不改变 ID。新 CLI 仍可原样使用支持多会话的旧 GUI 返回的完整长 ID。

用户说「这个会话」时，先查询当前会话一次，并将返回的 `session_id` 固定用于后续操作。窗口切到后台不会清除最后选中的会话，`window_focused` 表示查询时窗口是否位于前台。连续操作期间不要反复按焦点选择目标；用户切换窗格后，已经选定的 ID 不变。

```sh
whos-typing-cli session list --json
whos-typing-cli session current --json
SESSION_ID='将返回的 session_id 填入这里'
whos-typing-cli --session "$SESSION_ID" session read
whos-typing-cli --session "$SESSION_ID" 'pwd'
whos-typing-cli --session "$SESSION_ID" --timeout 30s 'sleep 60'
```

只有一个存活会话时，可以省略 `--session`。存在多个会话时，执行、读取、状态、交还、接入、分屏和聚焦都需要指定 ID；未知或已关闭的 ID 不会回退到其他会话。新 CLI 与旧 GUI 不兼容时会拒绝操作并提示升级，不发送待执行命令。不要自行关闭或重启用户的窗口。

- `session read` 是只读的，可在命令运行时使用，也不需要远端接入。它返回屏幕和保留的滚动历史末尾最多 10000 个 Unicode 码点；末尾重复行可能被合并，全屏程序的屏外内容不可见。不要把快照当作完整历史。
- 将整个 Shell 命令作为一个参数传给 CLI，按调用方 Shell 的规则安全引用，避免变量等在调用方提前展开。命令在绑定的 Shell 中执行；本地支持 fish、zsh 和 bash。输出合并了标准输出与标准错误，可能包含终端控制序列；完成时退出码反映命令结果。CLI 自身错误也会返回非零状态，应结合错误信息判断。
- 执行前确认会话和目标符合用户意图，尤其是远端目标。Shell 正忙或有未提交的人工输入时 CLI 会拒绝执行；不要绕过保护直接向 PTY 写入。需要交互输入的程序交由用户在 GUI 中操作。
- 超时或连接断开不代表命令已停止，结果可能未知；不要自动重试。请用户在 GUI 确认状态，必要时由用户按 Ctrl+C，再决定如何继续。

## 创建和聚焦

```sh
whos-typing-cli tab new --json
TAB_ID='将返回的 tab_id 填入这里'
whos-typing-cli tab rename --tab "$TAB_ID" '构建'
whos-typing-cli --session "$SESSION_ID" session split --direction right --json
whos-typing-cli --session "$SESSION_ID" session split --direction down --focus --json
whos-typing-cli --session "$SESSION_ID" session focus
```

创建结果包含新 `session_id` 和 `tab_id`。标签页默认显示最后选中窗格的 `username@hostname:/path`，不显示 ID 或编号。新标签追加到末尾，GUI 的 `Cmd+1` 至 `Cmd+9` 仅按当前位置切换。`tab rename` 按 `tab_id` 修改本次运行中的标签名，拒绝空白名称；人工名称不会被 Shell 标题、目录更新或窗格切换覆盖。`session list --json` 和 `session current --json` 返回 `tab_title`。CLI 创建默认保留原有焦点，只有用户需要时才使用 `--focus` 或 `session focus`。新会话是独立本地 Shell，不复制原会话的 SSH 连接。创建响应丢失时不要自动重试；先通过 `session list --json` 核对已有会话。

## 远端 Bash

默认标签标题只有在完成远端接入后才保证更新远端上下文；未接入 SSH 时可能仍显示本地或最近确认的信息，不能仅凭标题认定服务器身份。

只有用户在 GUI 中进入目标服务器的交互式 Bash 后，才能接入。确认目标及空提示符，然后运行：

```sh
whos-typing-cli --session "$SESSION_ID" attach --remote --shell bash --auto
```

保持该 CLI 进程运行。默认需要用户核对确认框中的会话 ID 和标签页 ID，再点击「确认接入」；可先点击「查看目标」核对空提示符。如果用户已在设置中开启「自动允许 Agent 接入远端 Shell」，包括首次接入都不弹确认框，窗口直接发送接入命令。自动允许不能验证远端身份，调用方仍须确认目标 Bash；不要为绕过拒绝而自行修改用户设置。等待 CLI 报告「远端 Bash 已接入」后，再执行远端命令。每个远端会话需接入一次，当前仅支持 Bash。接入会临时修改当前 Shell 的提示符和退出钩子，不修改 Shell 配置文件；告知仅允许只读操作的用户这一影响。

也可以运行不带 `--auto` 的 `whos-typing-cli --session "$SESSION_ID" attach --remote --shell bash`，由用户将 CLI 输出的完整命令粘贴到远端 Bash 空提示符并执行。自动接入等待确认期间，终端有新输出或人工输入时会停止；先重新确认目标和提示符，再发起新请求。

接入命令是一次性的；请求超时或失败后，重新接入须使用新生成的完整命令。手动模式若报接入输入不匹配，不要绕过校验或断言用户操作有误：确认复制内容和终端状态，结合 CLI 的诊断排查。失败的接入可能已留下临时提示符钩子；若出现提示符错误，先确认原有 Shell 状态，再与用户协调恢复，不要猜测原配置或自动重启会话。程序修复需要新 GUI 时，由用户决定何时结束原会话并重启。

发送远端命令前确认 GUI 仍处于预期目标：SSH 静默断线时，程序不能完全识别当前前台程序。若状态未知，请用户确认 GUI 中的目标和空提示符；必要时由用户按回车或重新接入，再继续。

## 状态诊断与失效恢复

使用 `whos-typing-cli --session "$SESSION_ID" session status --json` 查询控制方、远端接入状态、未知结果和建议动作。查询只读，不获取控制权。普通 `session status` 仍只输出 `agent`、`human` 或 `human_takeover`。旧窗口未提供诊断时会返回 `diagnostics_available: false`，不要将缺失字段理解为已就绪。

接入 Bash 因 `set -e` 等原因退出时，有效退出通知会结束 CLI 等待；缺少命令结束标记仍报告结果未知。钩子被覆盖或无法收到退出通知时，CLI 可能等到超时。共享 Shell 的选项会跨命令保留，不要假定上一条命令设置的 `set -e` 已自动清除。

旧请求等待中、超时或取消后，如果普通接入和交还被拒绝，请用户先确认目标窗格已回到交互式 Bash 空提示符，再点击「接管」和「恢复远端接入」。确认框会说明放弃旧请求等待；即使自动允许已开启，也必须单独确认恢复。新握手成功后再继续操作，不需要新开窗格或重启 SSH。

恢复不会重放旧命令、发送 Ctrl+C 或关闭 SSH，已标记为未知的业务结果仍为未知。`unknown_result` 可在恢复后继续为 true，不能据此认定新握手失败，也不能认定旧业务操作未执行。根据 `remote` 和 `suggested_action` 判断接入状态，重试业务操作前应另行核对结果。未提交输入、正在发送的命令字节或确认期间状态变化会阻止恢复；请用户核对后再申请，不自动反复执行 attach。
