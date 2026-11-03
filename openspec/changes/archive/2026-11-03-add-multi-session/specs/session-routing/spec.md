# Spec Delta

## Purpose

让 CLI 和 Agent 在多会话窗口中发现目标、创建工作区域并准确操作指定会话。通过稳定标识和明确的目标解析规则，保持用户界面焦点与命令目标相互独立，避免界面切换或版本差异导致命令发往错误终端。

## ADDED Requirements

### Requirement: 提供结构化会话发现

CLI SHALL 提供 `session list` 和 `session current`，支持 `--json`。列表 SHALL 返回每个存活会话的 `session_id`、`tab_id`、`tab_title`、Shell 标题和控制状态；当前会话查询 SHALL 返回聚焦会话的同类信息及 `window_focused`。查询 SHALL 不发送终端输入、不改变焦点、不获取控制权或重置空闲计时。

#### Scenario: 获取当前会话用于连续操作
- **WHEN** Agent 调用 `session current --json`
- **THEN** 返回查询时最后选中的存活会话 ID 和窗口前台状态
- **AND** Agent 可将该 ID 用于随后多次请求，不需要重复解析焦点

#### Scenario: 当前没有可用会话
- **WHEN** 当前会话不存在或应用无法连接
- **THEN** CLI 返回非零退出状态和明确错误，不猜测目标或创建会话

### Requirement: 显式指定会话目标

执行命令、读取快照、控制状态查询、交还控制权、远端接入、分屏和显式聚焦 SHALL 支持 `--session <id>`。仅有一个存活会话时 SHALL 允许省略目标；有多个会话时 SHALL 拒绝省略目标并提示使用会话列表选择。显式无效 ID SHALL 始终失败，不回退到当前或唯一会话。

#### Scenario: 多会话中省略目标
- **WHEN** 有多个会话且 CLI 未指定目标就执行命令
- **THEN** 返回非零退出状态，提示指定会话
- **AND** 所有会话均不接收该命令，控制状态不变

#### Scenario: 单会话兼容
- **WHEN** 仅有一个会话，调用原有命令执行、读取、状态、交还或接入命令且未指定 ID
- **THEN** 操作唯一会话并保留原有输出、退出码和超时语义

#### Scenario: 旧 ID 不回退
- **WHEN** 请求指定已关闭或未知会话 ID，即使仅剩一个会话
- **THEN** 返回目标不存在错误，不执行任何终端输入

### Requirement: 请求生命周期绑定目标

系统 SHALL 在接收请求时一次性解析并绑定目标会话，后续焦点、布局和会话数量变化 SHALL 不改变目标。不同会话的执行与输出 SHALL 独立，同一会话 SHALL 保留既有并发和人工输入保护。

#### Scenario: 执行期间切换窗格
- **WHEN** A 的命令正在执行，用户聚焦 B 或创建 C
- **THEN** 原请求继续返回 A 的结果，不向 B 或 C 发送输入

#### Scenario: 目标关闭与执行并发
- **WHEN** 请求绑定 A 后 A 被关闭
- **THEN** 未发送命令不会执行，已开始且结果无法确认的命令报告状态未知或会话结束
- **AND** 系统不自动重试、不重定向，不因此关闭其他会话的请求

### Requirement: 创建操作返回可寻址结果

CLI SHALL 提供 `tab new`、`session split --direction right|down` 和 `session focus`。创建操作 SHALL 支持 `--json`，成功返回新会话及标签页 ID；失败 SHALL 不留下空标签页或半完成布局。创建响应丢失时 SHALL 不自动重试，应允许通过列表核对。

#### Scenario: 创建后执行命令
- **WHEN** Agent 新建标签页或对指定会话分屏成功
- **THEN** 响应包含可直接用于命令请求的新会话 ID
- **AND** 创建本身不获取任何会话的 Agent 控制权

#### Scenario: 分屏目标在创建中关闭
- **WHEN** 分屏请求创建新 Shell 期间目标会话关闭
- **THEN** 分屏失败，释放新建资源，不在其他窗格完成分屏

### Requirement: 版本不兼容时拒绝目标操作

新版 CLI SHALL 在发送任何带会话目标的操作前确认服务支持多会话路由，未知能力时拒绝操作。确认和实际操作 SHALL 绑定同一个服务连接或等效实例标识。新版服务 SHALL 对旧版无目标请求执行相同的单会话兼容和多会话拒绝规则。

#### Scenario: 新 CLI 连接旧 GUI
- **WHEN** 新 CLI 指定会话 ID，但旧 GUI 不支持目标路由
- **THEN** CLI 提示升级 GUI，不发送待执行命令，也不降级为无目标请求

#### Scenario: 旧 CLI 连接多会话 GUI
- **WHEN** 旧 CLI 发出无目标请求，而新版 GUI 有多个会话
- **THEN** 服务拒绝请求，不选择当前聚焦会话代替

### Requirement: 按标签页 ID 重命名

CLI SHALL 提供 `tab rename --tab <tab_id> '标题'`，必须显式指定稳定标签页 ID，精确查找并更新名称，不以显示编号或标题代替 ID。成功重命名 SHALL 不改变 tab_id、session_id、运行中的命令或控制权。未知或已关闭 ID，以及空或纯空白名称 SHALL 失败，不修改其他标签页或焦点。

#### Scenario: 重命名后台标签页
- **WHEN** CLI 指定后台标签页的有效 ID 和非空白标题
- **THEN** 对应标签页名称更新，当前标签页和窗格焦点保持不变
- **AND** `session list --json` 和 `session current --json` 的 `tab_title` 反映各自标签页名称

#### Scenario: 拒绝错误重命名目标
- **WHEN** CLI 省略 tab_id、指定未知或已关闭的 tab_id，或提供空白名称
- **THEN** 返回非零退出状态，原名称和焦点保持不变
- **AND** 即使只有一个标签页，也不回退到该标签页

#### Scenario: 同一标签页的会话查询名称一致
- **WHEN** 含多个窗格的标签页被重命名为 `发布检查`
- **THEN** 列表中该页所有会话的 tab_title 均为 `发布检查`，其他标签页名称不变
- **AND** 标签页和各会话的 ID 不变
