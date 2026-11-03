# Spec Delta

## MODIFIED Requirements

### Requirement: 保持当前会话状态

终端 SHALL 在每个会话存续期间复用该会话独立的 Shell，保留连续命令之间的工作目录与环境变量修改。标签页切换和分屏尺寸变化 SHALL 不重建会话或混用其他会话的状态。

#### Scenario: 连续执行命令
- **WHEN** 用户执行 `cd /tmp`，再执行 `pwd`，随后执行 `export WT_MVP=ok` 与 `echo "$WT_MVP"`
- **THEN** 工作目录为系统实际解析的临时目录，变量输出为 `ok`

#### Scenario: 多会话隔离
- **WHEN** 用户在 A 修改工作目录和环境变量，然后切换到 B 再返回 A
- **THEN** A 保留修改，B 的工作目录和环境变量不因 A 的操作变化

### Requirement: 结束会话并释放资源

应用 SHALL 在用户关闭窗口时关闭全部终端连接、结束全部 Shell 会话并退出应用。单个 Shell 自行退出 SHALL 只关闭对应窗格并释放该会话资源；最后一个会话结束时 SHALL 关闭窗口并退出应用。

#### Scenario: 用户关闭窗口
- **WHEN** 用户关闭当前终端窗口
- **THEN** 全部标签页的 Shell 会话与终端资源被释放，应用进程结束

#### Scenario: 用户退出 Shell
- **WHEN** 用户在 Shell 中执行 `exit`
- **THEN** 对应窗格关闭且资源被释放，其他存活会话继续运行
- **AND** 若没有其他会话，窗口关闭且应用进程结束

### Requirement: 报告启动失败

应用 SHALL 在首次终端依赖加载或初始 Shell 启动失败时向启动入口的标准错误输出说明原因，并以非零状态退出。运行中新增会话失败 SHALL 报告给操作发起方、释放本次创建的资源，并保留既有布局和会话。

#### Scenario: 启动失败
- **WHEN** 应用首次启动时运行依赖无法加载，或配置的 Shell 无法启动
- **THEN** 用户能从启动输出看到失败原因，应用不会留下可交互的空白窗口或本次创建的 Shell 会话

#### Scenario: 运行中新增失败
- **WHEN** GUI 或 CLI 新建标签页或分屏时 Shell 启动失败
- **THEN** GUI 显示失败原因或 CLI 返回非零退出状态，已分配资源被释放
- **AND** 既有会话、布局、焦点和未提交输入不变
