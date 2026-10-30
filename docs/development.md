# 本地开发与验收

whos-typing MVP 使用 Go 和 MyGo 原生 UI，在一个窗口中运行本地用户 Shell。本版仅提供人工终端交互。

## 环境与依赖

- 首个验收平台为 macOS，需要可用的桌面会话。
- Go 版本要求为 1.27.3 或更高，依赖版本以 go.mod 为准。
- MyGo 使用原始上游 `github.com/egoist/mygo v0.7.3`，不使用 fork、本地 `replace` 或 go.work 替换。
- 原生终端插件使用 libghostty-vt。开发启动时由 MyGo 获取并校验对应原生库，首次运行需要网络访问；不需要自行编译 Ghostty。

## 构建

在仓库根目录执行 `make build`，构建产物位于 `output/whos-typing`。

## 启动

在仓库根目录执行：

```sh
make run
```

命令先构建应用，再打开标题为 `Who's Typing` 的原生终端窗口。终端采用用户 Shell，键盘输入直接传给当前会话。

## 检查

```sh
GOWORK=off go list -m -json github.com/egoist/mygo
CGO_ENABLED=0 GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test ./...
openspec validate add-native-terminal-mvp --strict
```

依赖信息中的 `Path` 应为 `github.com/egoist/mygo`，`Version` 应为 `v0.7.3`，不得出现 `Replace`。代码提交前使用 `gofmt` 格式化修改的 Go 文件。

## 交互验收

在真实窗口中按顺序检查：

1. 启动后直接输入 `echo mvpx`，按退格删除 `x`，再按回车。预期输出 `mvp`，并能继续输入。
2. 输入 `printf 'begin\n'; sleep 2; printf 'end\n'`。预期先看到 `begin`，命令结束时再看到 `end`。
3. 执行 `cd /tmp` 与 `pwd`。预期目录保持为系统实际解析的临时目录。
4. 执行 `export WT_MVP=ok` 与 `echo "$WT_MVP"`。预期输出 `ok`。
5. 执行 `sleep 60`，按 Ctrl+C。预期等待被中断，随后能执行 `echo resumed`。
6. 执行 `exit`。预期窗口关闭且应用进程结束。
7. 重新启动，记录 Shell PID，再关闭窗口。fish 使用 `echo $fish_pid`，zsh/bash 使用 `echo $$`。预期应用及该 Shell 结束。

## 退出与启动失败

关闭窗口会结束当前终端会话。Shell 自行退出也会关闭应用；本版没有后台常驻或会话恢复。用户主动脱离会话的后台进程不在应用管理范围内。

若原生库加载或 Shell 启动失败，应用应向标准错误输出原因并以非零状态退出。先查看启动命令的输出，区分依赖下载、动态库加载和 Shell 路径错误。失败验证应只修改测试进程的环境，不修改用户 Shell 配置。

## 验证记录

2026-10-30 在 macOS arm64、Go 1.27.3 下验证：

- 主会话独立核对 MyGo 版本和校验和，确认没有本地替换；格式、构建、vet 与单元测试通过。
- 通过 MyGo 自动化输入路径在真实窗口中验证退格编辑、执行命令、持续输出、工作目录和环境变量保留、Ctrl+C。主会话已回读验收截图。
- 执行 `exit` 后应用与默认 fish Shell 均退出。
- 通过测试进程的 `MYGO_GHOSTTY_VT` 和 `SHELL` 分别指向不存在的路径，验证失败原因输出和退出码 1。
- 单次启动确认窗口处于前台且终端节点聚焦；点击系统关闭按钮后应用和直接 Shell 进程均结束。
- 系统键盘注入路径存在待排查问题：System Events 输入的空格和回车未生效，官方 MyGo v0.7.3 终端示例也复现。独立 Cocoa 控件收到了对应按键事件，但当前输入法组合态仍可能影响 MyGo 的处理，尚不能确定根因。自动化文本输入路径的成功不能替代系统键盘验收，本版尚未完成全部交互验收。

本次检查将 Go 模块缓存和构建缓存放在 `/private/tmp` 下。GUI 验证使用上游自动化插件，只在 `e2e` 构建标签下启用；正常构建不包含该测试入口。

```sh
CGO_ENABLED=0 GOWORK=off go build -tags e2e -o /tmp/whos-typing-e2e ./cmd/whos-typing
MYGO_AUTOMATION=/tmp/whos-typing-e2e.sock /tmp/whos-typing-e2e
```

自动化协议见上游 MyGo 的 `docs/plugins/automation.md`，可读取窗口、发送键盘事件和捕获窗口画面。本次原生库取自官方发行文件并核验 SHA-256，使用 `MYGO_GHOSTTY_VT` 指向临时文件，不修改用户缓存。
