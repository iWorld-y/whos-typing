# Spec Delta

## MODIFIED Requirements

### Requirement: 输入并执行命令

终端 SHALL 在人类持有控制权时将用户键盘和粘贴输入交给当前 Shell，支持编辑尚未提交的输入，并在用户按回车后由 Shell 执行命令。Agent 持有控制权时，终端 SHALL 暂停普通人工输入，同时保留现有 Ctrl+C 中断行为。

#### Scenario: 修改并执行输入
- **WHEN** 用户输入 `echo mvpx`，按退格删除最后的 `x`，再按回车
- **THEN** Shell 执行 `echo mvp`，终端显示输出 `mvp`
- **AND** 命令结束后用户能够继续输入

#### Scenario: Agent 控制期间的普通输入
- **WHEN** Agent 持有控制权，用户在终端输入普通字符或粘贴内容
- **THEN** 普通字符和粘贴内容不发送到 Shell，也不在之后自动回放
- **AND** 用户仍可通过「接管」获得输入控制权
