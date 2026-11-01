# local-terminal Specification

## Purpose

为用户提供可直接操作本机 Shell 的原生终端窗口，使用户能够在同一个会话中输入命令、查看持续输出并保留工作目录等状态，同时明确启动失败和会话结束时的可见行为。

## Requirements

### Requirement: 打开原生终端窗口

应用 SHALL 在支持的 macOS 环境中打开一个原生终端窗口，启动本地用户 Shell，并使终端获得键盘焦点。

#### Scenario: 启动应用
- **WHEN** 用户在满足运行依赖的环境中启动应用
- **THEN** 应用显示一个标题为 `Who's Typing` 的原生窗口，并显示本地 Shell 的初始输出
- **AND** 用户无需先点击额外的输入框即可输入命令

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

### Requirement: 显示持续输出

终端 SHALL 在程序运行期间持续显示其输出，不等待程序退出后才一次性呈现结果。

#### Scenario: 长命令分次输出
- **WHEN** 用户执行 `printf 'begin\n'; sleep 2; printf 'end\n'`
- **THEN** 用户在命令结束前看到 `begin`，随后看到 `end`

### Requirement: 保持当前会话状态

终端 SHALL 在窗口存续期间复用同一个 Shell，保留连续命令之间的工作目录与环境变量修改。

#### Scenario: 连续执行命令
- **WHEN** 用户执行 `cd /tmp`，再执行 `pwd`，随后执行 `export WT_MVP=ok` 与 `echo "$WT_MVP"`
- **THEN** 工作目录为系统实际解析的临时目录，变量输出为 `ok`

### Requirement: 中断前台命令

终端 SHALL 将 Ctrl+C 交给前台终端程序，使用户能够中断可响应该按键的命令并继续使用 Shell。

#### Scenario: 中断等待命令
- **WHEN** 用户执行 `sleep 60` 后按 Ctrl+C
- **THEN** 等待命令结束，用户能够在同一窗口执行下一条命令

### Requirement: 结束会话并释放资源

应用 SHALL 在用户关闭窗口时关闭终端连接、结束本次 Shell 会话并退出应用；Shell 自行退出时也 SHALL 关闭窗口并退出应用。

#### Scenario: 用户关闭窗口
- **WHEN** 用户关闭当前终端窗口
- **THEN** 本次 Shell 会话与终端资源被释放，应用进程结束

#### Scenario: 用户退出 Shell
- **WHEN** 用户在 Shell 中执行 `exit`
- **THEN** 窗口关闭，终端资源被释放，应用进程结束

### Requirement: 报告启动失败

应用 SHALL 在终端依赖加载或 Shell 启动失败时向启动入口的标准错误输出说明原因，并以非零状态退出。

#### Scenario: 启动失败
- **WHEN** 运行依赖无法加载，或配置的 Shell 无法启动
- **THEN** 用户能从启动输出看到失败原因，应用不会留下可交互的空白窗口或本次创建的 Shell 会话
