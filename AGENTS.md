# whos-typing 项目指引

## 构建与运行

所有命令从仓库根目录执行。项目使用 Go 1.27.3、上游 `github.com/egoist/mygo v0.7.4` 和 MyGo 原生终端插件；首个验收平台为 macOS。

```sh
GOWORK=off go list -m -json github.com/egoist/mygo
CGO_ENABLED=0 GOWORK=off go build ./...
make build
make run
```

`make build` 生成 `output/whos-typing` 和 `output/whos-typing-cli`；`make run` 会先构建，再启动 GUI。终端依赖 libghostty-vt，获取方式和运行条件见 `docs/development.md`。

## 测试与检查

```sh
GOWORK=off go test -count=1 ./...
GOWORK=off go test -count=1 -run '^TestSessionReadUnicodeLimit$' ./internal/control
GOWORK=off go test -race ./internal/app ./internal/control ./internal/shell
GOWORK=off go vet ./...
openspec validate --specs --strict
```

测试与实现放在同一包的 `_test.go` 文件中。部分真实终端和 fish 测试要求 `MYGO_GHOSTTY_VT` 指向可用的上游原生库；未设置时会跳过，检查结果时需区分跳过与通过。GUI 自动化入口仅在 `e2e` 构建标签下启用。

## 项目结构

- `cmd/whos-typing/`：GUI 程序入口；`cmd/whos-typing-cli/`：命令行入口。
- `internal/app/`：MyGo 窗口、终端和应用生命周期。
- `internal/control/`：本地 Unix socket、CLI 协议和会话文本读取。
- `internal/shell/`：PTY、Shell 状态、命令输入输出及远端 Bash 接入。
- `docs/development.md`：构建、使用和验收说明；`openspec/`：规格及变更文档。

## 实现约定

- 保持入口层负责启动和编排，终端状态与协议逻辑放在对应的 `internal` 包中。
- 遵循现有 Go 格式和中文注释；使用 `gofmt`。错误保留上下文和原因，例如 `return nil, fmt.Errorf("启动 Shell 失败: %w", err)`。
- 本地 CLI 命令复用 GUI 的 Shell 会话；人工输入、并发请求及 Ctrl+C 的处理依赖 `internal/shell/` 的状态和标记，不要绕过它们直接写入 PTY。
- `session read` 读取屏幕和滚动历史的文本快照，不向 Shell 注入输入；其 10000 个 Unicode 码点上限及末尾重复行处理位于 `internal/control/session_read.go`。
- 远端接入仅支持交互式 Bash。用户确认目标和空提示符后，可由窗口自动输入接入命令，也可由用户手动执行；不得向密码提示或交互程序注入输入。超时或连接断开时，命令结果可能未知，不应自动重试或假定命令已停止。

## 边界

- 未经用户明确授权，不在用户主目录中搜索文件或目录。
- 验证使用临时目录、独立 Shell 和 socket。未经用户明确授权，不读取、关闭、重启或向正在使用的 GUI/SSH 会话注入测试输入。
- 本地控制服务使用当前用户私有目录及 socket，并以锁阻止多个窗口抢占；修改协议或生命周期时保留这些隔离和关闭行为。
- 保持 MyGo 指向原始上游依赖；不要引入 fork、本地 `replace` 或 `go.work` 替换。
- `output/` 是构建产物，不纳入提交。提交遵循 Conventional Commits，描述使用中文；是否提交或推送以用户要求为准。
