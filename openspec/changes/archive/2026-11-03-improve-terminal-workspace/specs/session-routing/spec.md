# Spec Delta

## MODIFIED Requirements

### Requirement: 提供结构化会话发现

CLI SHALL 提供 `session list` 和 `session current`，支持 `--json`。新服务的列表 SHALL 直接返回每个存活会话的完整 `session_id`、`tab_id`、`tab_title`、Shell 标题和控制状态，不要求调用者截取 ID。会话 ID SHALL 使用 `session_01` 起始的独立十进制序列，标签 ID SHALL 使用 `tab_01` 起始的独立十进制序列；数字部分至少两位，超过 99 后自然增长。当前查询 SHALL 返回同类信息及 window_focused。tab_title SHALL 与 GUI 的当前动态默认名称或人工名称一致。查询 SHALL 不发送输入、不改变焦点或控制权、不重置空闲计时。

#### Scenario: 获取当前会话用于连续操作
- **WHEN** Agent 调用 `session current --json`
- **THEN** 返回最后选中的存活会话短 ID 和窗口前台状态
- **AND** Agent 可将该 ID 用于随后多次请求，不需要重复解析焦点

#### Scenario: 当前没有可用会话
- **WHEN** 当前会话不存在或应用无法连接
- **THEN** CLI 返回非零退出状态和明确错误，不猜测目标或创建会话

#### Scenario: 多分屏列表中的标识和标题
- **WHEN** Agent 查询包含多个标签与分屏的新服务列表
- **THEN** 文本和 JSON 均直接显示带类型前缀的完整标签、会话标识，同标签的 tab_id 和 tab_title 一致，各会话 session_id 不同
- **AND** 名称或位置变化不改变这些标识

### Requirement: 显式指定会话目标

执行命令、读取快照、控制状态查询、交还控制权、远端接入、分屏和显式聚焦 SHALL 支持 `--session <id>`，精确匹配服务返回的完整标识，不将位置、标题或不完整前缀作为目标。仅有一个存活会话时 SHALL 允许省略目标；多个会话时 SHALL 拒绝省略目标并提示选择。显式无效 ID SHALL 始终失败，不回退到当前或唯一会话。

#### Scenario: 多会话中省略目标
- **WHEN** 有多个会话且 CLI 未指定目标就执行命令
- **THEN** 返回非零退出状态，提示指定会话
- **AND** 所有会话均不接收该命令，控制状态不变

#### Scenario: 单会话兼容
- **WHEN** 仅有一个会话，调用原有执行、读取、状态、交还或接入命令且未指定 ID
- **THEN** 操作唯一会话并保留原有输出、退出码和超时语义

#### Scenario: 旧 ID 不回退
- **WHEN** 请求指定已关闭或未知会话 ID，即使仅剩一个会话
- **THEN** 返回目标不存在错误，不执行任何终端输入

#### Scenario: 拒绝不完整标识或位置别名
- **WHEN** Agent 传入未被分配为完整 ID 的位置数字、标题或短 ID 前缀
- **THEN** 服务拒绝请求，不推测最接近或唯一前缀匹配的会话

### Requirement: 创建操作返回可寻址结果

CLI SHALL 提供 `tab new`、`session split --direction right|down` 和 `session focus`。创建操作 SHALL 支持 `--json`，新服务成功返回可直接寻址的新会话完整 ID 及所属标签页完整 ID；失败 SHALL 不留下空标签页或半完成布局。创建响应丢失时 SHALL 不自动重试，应允许通过列表核对。

#### Scenario: 创建后执行命令
- **WHEN** Agent 新建标签页或对指定会话分屏成功
- **THEN** 响应包含可直接用于命令请求的新会话短 ID
- **AND** 创建本身不获取任何会话的 Agent 控制权

#### Scenario: 分屏目标在创建中关闭
- **WHEN** 分屏请求创建新 Shell 期间目标会话关闭
- **THEN** 分屏失败，释放新建资源，不在其他窗格完成分屏

## ADDED Requirements

### Requirement: 短标识升级保持旧服务兼容

CLI SHALL 将服务返回的标识视为不透明字符串，继续支持已通过现有多会话能力检查的旧 GUI 返回的完整长 ID，不截断或强制限定参数格式和位数。新服务 SHALL 使用短 ID；升级重启前已结束会话的旧标识 SHALL 失败，不映射到新会话。原有能力握手、连接绑定和错误行为 SHALL 保持不变。

#### Scenario: 新 CLI 操作旧 GUI
- **WHEN** 旧 GUI 支持多会话并返回完整长 ID
- **THEN** 新 CLI 可原样传递该 ID 完成原有目标操作，不截取为短 ID

#### Scenario: GUI 升级后拒绝旧会话标识
- **WHEN** GUI 重启升级后，Agent 使用重启前返回的旧会话 ID
- **THEN** 服务返回目标不存在，不以标题、位置或前缀重新绑定目标
