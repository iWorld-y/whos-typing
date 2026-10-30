# Tasks

## 1. 初始化应用与上游依赖

- [x] 1.1 初始化 Go 模块与 `cmd/whos-typing`、`internal/app` 最小结构，固定原始上游 `github.com/egoist/mygo` 的版本或提交；通过 `GOWORK=off go list -m -json github.com/egoist/mygo` 确认无本地或 fork 替换，并确认该版本包含原生终端插件。
- [x] 1.2 接入应用启动入口与错误返回，入口仅保留编排和错误输出；通过 `CGO_ENABLED=0 GOWORK=off go build ./cmd/whos-typing` 验证工具链和依赖可构建。
- [x] 1.3 编写 `docs/development.md`，记录选定上游版本、Go 要求、macOS 运行条件、libghostty-vt 获取方式及启动命令；逐条核对文档命令与实际依赖配置一致。

## 2. 原生终端交互

- [x] 2.1 使用 MyGo 原生 UI 创建标题为 `Who's Typing` 的单窗口，终端填满窗口并自动聚焦；启动应用，验证可见终端与直接键盘输入。
- [x] 2.2 使用终端插件运行默认用户 Shell，保留同一会话并转发键盘输入与输出；执行规格中的退格修改、分次输出、目录切换、环境变量与 Ctrl+C 场景，确认结果符合预期。
- [x] 2.3 在开发文档中加入实际交互验收步骤与预期输出，并记录单窗口、本地 Shell 的范围；按文档复跑命令确认可操作。

## 3. 生命周期与失败处理

- [x] 3.1 实现窗口关闭、Shell 退出和应用退出的资源清理，处理 Shell 提前退出与重复关闭；对新增生命周期逻辑编写定向测试，并验证关闭后应用与其直接创建的 Shell 结束。
- [x] 3.2 处理原生库加载和 Shell 启动失败，将原因输出到标准错误并非零退出；通过隔离测试配置模拟失败，验证错误可见且资源释放，不修改用户 Shell 配置。
- [x] 3.3 在开发文档中写明退出行为、启动失败排查方式和后台进程管理边界；与实际失败测试和关闭行为核对一致。

## 4. 集成验收

- [x] 4.1 执行 `gofmt`、`CGO_ENABLED=0 GOWORK=off go build ./...`、`GOWORK=off go vet ./...` 和 `GOWORK=off go test ./...`；修复本次引入的问题并记录实际结果。
- [x] 4.2 在真实 macOS 桌面中完成 `local-terminal` 全部场景，记录窗口启动、输入输出、状态保留、中断、两种退出方式与失败路径的结果；有未执行项时保留未完成状态。
- [x] 4.3 运行 `openspec validate add-native-terminal-mvp --strict` 并核对实现范围；确认未引入 SSH、CLI + Skill、Agent 接入或后台服务后更新任务状态。
