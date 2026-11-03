# Who's Typing

Who's Typing 是一个人与 Agent 共用的原生终端。你可以直接在窗口中操作，也可以让外部 Agent 通过 CLI 在同一个会话里执行命令。Agent 操作时，你能实时看到命令和输出。

## 为什么做这个产品

你可能已经在终端里进入项目目录、设置环境变量，或通过 SSH 登录服务器。当你希望 Agent 接着处理工作时，它需要知道当前状态，并在已有会话中继续操作。

Who's Typing 希望让这种协作成为终端的日常用法。你负责需要人工处理的步骤，Agent 通过命令行接口继续工作。双方使用同一个 Shell，保留连续操作所需的状态，你也能在窗口里看到工作如何推进。

Who's Typing 这个名字来自「谁在我的终端里打字」的想法。窗口中的命令可以由你输入，也可以来自你正在使用的 Agent。

## 设计理念

### 共用一个会话

人工操作和 Agent 操作应当能衔接起来。你在窗口中切换目录，Agent 随后执行命令时就使用这个目录；Agent 修改会话中的环境变量，你继续操作时也能使用它。连接远端服务器并完成 Bash 接入后，Agent 可以继续使用当前远端会话。

### 让操作过程实时可见

终端窗口持续显示命令和输出，让你能在执行过程中判断进展。CLI 在命令完成后返回输出和退出状态，供 Agent 决定下一步。人和 Agent 通过各自的入口了解同一会话的状态。

共用会话也需要明确输入顺序。首版会在 Shell 忙碌或存在未提交的人工输入时拒绝 CLI 命令；CLI 执行期间暂停普通键盘和粘贴输入，用户仍可在窗口中按 Ctrl+C 中断前台命令。

### 通过 CLI + Skill 接入外部 Agent

我们希望 Agent 像调用普通命令行工具一样使用 Who's Typing。CLI 提供执行命令、读取会话和接入远端 Bash 的能力；Skill 用于说明调用方法、操作顺序和结果处理，让外部 Agent 沿用自己的命令执行工具来操作终端。

项目提供 [CLI Skill](skills/whos-typing-cli/SKILL.md)，说明如何读取和操作共享会话。
Skill 目录中的 `WHOS_TYPING_SKILL` 用于标识来源，分发时需一并保留。

## 当前版本

- 在原生窗口中运行本地 Shell，保留会话中的工作目录和环境变量。
- 通过 CLI 在同一会话中执行命令，获取终端输出和退出码。
- 通过 CLI 读取当前屏幕和终端保留的滚动历史。
- 手动接入已建立的远端 Bash 会话，在其中使用 CLI 执行命令。

首版采用单窗口、单会话。CLI 使用时需保持 GUI 运行，关闭窗口会结束本次 Shell 会话。当前没有后台常驻或会话恢复。

## 环境要求

- macOS 为首个验收平台，需要桌面环境和 Go 1.27.3 或更高版本。
- 原生界面使用上游 [MyGo](https://github.com/egoist/mygo) `v0.7.4`。终端插件依赖 libghostty-vt。从源码运行时，MyGo 首次获取并校验原生库，需要网络连接；打包后的应用包含该库。

## 快速开始

在项目根目录启动：

```sh
make run
```

该命令先构建程序，再在后台启动终端窗口。应用日志追加到 `output/whos-typing.log`。也可以单独执行 `make build`，产物位于 `output/whos-typing` 和 `output/whos-typing-cli`。

运行 `./output/whos-typing-cli --help` 查看用法，或运行 `./output/whos-typing-cli --version` 查看版本。这两个命令不需要启动 GUI。

macOS 应用包启动后会尝试将 CLI 链接到 `/usr/local/bin/whos-typing-cli`，并将随包的 Skill 链接到 `~/.agents/skills/whos-typing-cli`。需要管理员权限时，应用会先询问；已有同名命令或其他来源的 Skill 时不会覆盖。已有本项目标记的 Skill 目录会保留原样。请先将应用从 DMG 拖入应用程序文件夹，再启动应用。

保持窗口运行，在启动命令所在的终端或其他终端中执行：

```sh
./output/whos-typing-cli 'pwd'
./output/whos-typing-cli 'cd /tmp'
./output/whos-typing-cli 'pwd'
```

命令在 GUI 的同一个 Shell 会话中执行，因此第二次 `pwd` 会使用 `cd /tmp` 后的工作目录。GUI 会显示命令和输出；命令结束后，CLI 返回输出和退出码。终端合并标准输出和标准错误，结果可能包含终端控制序列。

本地 CLI 执行支持 fish、zsh 和 bash。Shell 正忙或存在尚未提交的人工输入时，CLI 会拒绝执行。

让外部 Agent 使用时，将 CLI 的实际路径和当前会话说明提供给它。Agent 可以先通过 `session read` 了解终端内容，再调用 CLI 执行命令，并根据输出和退出状态继续处理。

### 读取会话

```sh
./output/whos-typing-cli session read
```

读取不会向终端发送输入，命令运行期间也可使用。返回结果会移除首尾换行，并保留末尾最多 10000 个 Unicode 码点。末尾连续多行完全相同时，如果合并后更短，会显示为 `内容 (重复 N 行)`，其中 `N` 是原有总行数。

结果是终端文本快照，不包含颜色等显示样式。全屏程序可能只绘制当前屏幕；没有输出到终端的屏外内容无法读取。可以先在 GUI 中翻页或放大窗口，再读取当前画面。

### 接入远端 Bash

在 GUI 中进入目标服务器的交互式 Bash 后，确认当前是空提示符，然后在另一个终端运行：

```sh
./output/whos-typing-cli attach --remote --shell bash
```

将 CLI 输出的完整接入命令粘贴到 GUI 的远端 Bash 提示符并执行，等待 CLI 显示「远端 Bash 已接入」。之后可以使用 CLI 在该远端会话中执行命令。每个远端会话需要接入一次；读取会话文本不需要接入。

接入命令应在目标 Bash 提示符执行，不要粘贴到堡垒机菜单或密码提示。发送远端命令前也应确认目标：如果 SSH 静默断线，或菜单输出尚未被读取，程序无法完全确认当前前台程序。

### 命令等待与中断

可以设置 CLI 的等待时间：

```sh
./output/whos-typing-cli --timeout 30s 'sleep 60'
```

CLI 超时或断开不代表命令已停止，结果可能未知。先在 GUI 中确认状态，必要时按 Ctrl+C；不要直接重试。CLI 执行期间，GUI 暂停普通键盘和粘贴输入，因此需要交互输入的程序应直接在 GUI 中操作。

## 开发

在 macOS 上执行 `make package`，可以在 `output/` 生成当前 Go 架构的 DMG。打包要求和签名说明见 [开发文档](docs/development.md)。

发布前，在 `mygo.json` 中更新版本号并推送到 `master`；工作区干净且与远端一致时，执行 `make release`。脚本会推送版本标签，由 GitHub Actions 构建、校验 DMG 并创建 Release。

```sh
GOWORK=off go test ./...
GOWORK=off go vet ./...
openspec validate --specs --strict
```

部分真实终端和 fish 测试需要 `MYGO_GHOSTTY_VT` 指向可用的上游原生库，未设置时会跳过。更多构建、测试与验收说明见 [docs/development.md](docs/development.md)。
