# 本地开发与验收

whos-typing 使用 Go 和 MyGo 原生 UI，在一个窗口中运行多个独立的本地 Shell 会话。用户可以直接操作终端，也可以通过 CLI 将命令发送到指定会话。

## 环境与依赖

- 首个验收平台为 macOS，需要可用的桌面会话。
- Go 版本要求为 1.27.3 或更高，依赖版本以 go.mod 为准。
- MyGo 使用原始上游 `github.com/egoist/mygo v0.7.4`，不使用 fork、本地 `replace` 或 go.work 替换。
- 原生终端插件使用 libghostty-vt。开发启动时由 MyGo 获取并校验对应原生库，首次运行需要网络访问；不需要自行编译 Ghostty。

## 构建

在仓库根目录执行 `make build`，构建产物为 `output/whos-typing` 和 `output/whos-typing-cli`。

在 macOS 上执行 `make package`，为当前 Go 架构生成 `output/whos-typing-0.1.0-<架构>.dmg`。镜像包含 GUI、CLI、Skill 和终端原生库。打包使用项目依赖的 MyGo CLI；首次获取依赖或原生库时需要网络连接。应用使用临时签名，尚未经过 Apple 公证。

## 启动

在仓库根目录执行：

```sh
make run
```

命令先构建应用，再在后台启动标题为 `Who's Typing` 的原生终端窗口。命令返回后可以继续使用当前终端，应用日志追加到 `output/whos-typing.log`。终端采用用户 Shell，键盘输入直接传给当前会话。

## 管理标签页和分屏

窗口顶部显示标签页和左右滚动标签的按钮。按 `Cmd+T` 新建标签页，按 `Cmd+D` 对当前窗格向右分屏，按 `Cmd+Shift+D` 向下分屏。快捷键由应用处理，不会作为输入发送给终端。拖动分隔线可调整比例，分屏后的窗格可以继续分屏。切换标签页会恢复该页最后选中的存活窗格。

在标签页上点击鼠标右键，可以关闭该标签页或该页最后选中的窗格。终端区域保留原有右键菜单。标签页默认按本次应用的创建顺序命名为 `01`、`02` 等，关闭后不会重排或复用编号。重启应用后编号重新从 `01` 开始。

GUI 创建后会聚焦新会话。关闭窗格只结束该窗格的会话；关闭标签页会结束该页的全部会话。其他会话继续运行。最后一个会话结束后，窗口关闭并退出应用。

新会话使用用户登录 Shell 和用户目录，不复制现有会话的目录修改、环境变量或 SSH 连接。后台标签页继续读取输出并维护各自的控制权计时。新增会话启动失败时，现有会话和布局不变。

## 发现和创建会话

```sh
./output/whos-typing-cli session list --json
./output/whos-typing-cli session current --json
SESSION_ID='将返回的 session_id 填入这里'
./output/whos-typing-cli tab new --json
TAB_ID='将返回的 tab_id 填入这里'
./output/whos-typing-cli tab rename --tab "$TAB_ID" '构建'
./output/whos-typing-cli --session "$SESSION_ID" session split --direction right --json
./output/whos-typing-cli --session "$SESSION_ID" session split --direction down --focus --json
./output/whos-typing-cli --session "$SESSION_ID" session focus
```

列表返回每个会话的 `session_id`、`tab_id`、`tab_title`、Shell 标题和控制状态，并报告当前会话及 `window_focused`。当前会话是最后选中的终端；窗口失去前台焦点或操作工具栏时保留该选择。处理「这个会话」时，先查询一次当前会话，再用返回的 ID 执行整组操作。

`tab rename` 根据稳定 `tab_id` 重命名，拒绝空白名称。重命名不改变焦点，Shell 设置标题时也不会覆盖标签页名称；名称仅在本次应用运行期间保留。

CLI 创建默认不改变标签页或终端焦点，指定 `--focus` 才聚焦新会话。创建结果返回新 `session_id` 和 `tab_id`。如果创建响应丢失，不要自动重试；先检查会话列表。

只有一个存活会话时可以省略 `--session`。存在多个会话时，执行、读取、控制、接入、分屏和聚焦必须指定会话 ID。未知或已关闭的 ID 不会回退到其他会话。新 CLI 会先在同一连接上确认窗口支持多会话；不兼容时拒绝操作并提示升级窗口。

## 使用 CLI 执行命令

先启动 GUI，再在另一个终端执行：

```sh
./output/whos-typing-cli 'pwd'
./output/whos-typing-cli 'cd /tmp'
./output/whos-typing-cli 'pwd'
```

CLI 将一个字符串交给绑定的 Shell，窗口会显示命令和输出。命令完成后，CLI 输出同一终端会话中的命令结果，并返回命令的退出状态。示例中的第二条 `pwd` 使用前一条命令设置的工作目录。

当前支持 fish、zsh 和 bash。其他 Shell 仍可在 GUI 使用，但 CLI 会报告不支持。GUI 尚未启动、Shell 正忙、有人工输入尚未提交，或程序无法确认终端空闲时，CLI 会报告原因并以非零状态退出。

终端将标准输出和标准错误合并，CLI 返回的是这一路终端输出，可能包含换行转换和终端控制序列。fish 的 CLI 输出会过滤目录更新序列 OSC 7，包括命令主动打印的相同序列；GUI 仍会处理该序列。后台进程与当前命令共用终端时，输出可能混在一起，注入命令的短暂时段也可能隐藏后台文字输出。

通过 CLI 发送命令或自动接入输入时，终端将调用方视为 Agent，手工运行 CLI 也使用相同规则。Agent 持有控制权期间，GUI 暂停普通键盘和粘贴输入，Ctrl+C 仍可用于中断前台命令。CLI 自身断开时，GUI 中已启动的命令不会因此自动停止。

通信使用当前用户私有的本地 Unix socket。运行时会在私有目录中临时保存命令和捕获的输出，正常结束后删除；进程异常终止时，临时文件可能保留在 `/tmp/whos-typing-<用户 ID>`。

zsh 和 bash 通过 `source` 执行命令，fish 在当前 Shell 中通过 `eval` 执行。依赖脚本上下文的命令可能与直接键入不同；如果 Shell 退出时未发出完成标记，CLI 会报告命令未完成。用户配置移除提示符钩子后，已完成的命令仍返回结果，但后续 CLI 请求可能被拒绝。

多个会话时，在命令前添加 `--session "$SESSION_ID"`。可以用 `--` 结束顶层选项，例如 `./output/whos-typing-cli --session "$SESSION_ID" -- '--help'`。会话刚启动时，如果 Shell 尚未到达空提示符，CLI 会拒绝执行并报告尚未就绪。

## 切换终端控制权

Agent 控制期间，终端显示蓝色粒子效果，顶部中央的半透明毛玻璃胶囊显示「agent 正在控制」和「接管」。点击「接管」后，人类立即取得输入控制权；已经开始的命令继续运行，可以由人类与前台程序交互或按 Ctrl+C。接管期间，胶囊显示「人类正在控制」和「交还 Agent」；新的 CLI 命令和自动接入输入会被拒绝，`session read` 仍可读取终端内容。

人类完成操作后，点击「交还 Agent」。如果终端有未提交输入、前台程序仍在运行或状态无法确认，先完成输入并等待空提示符。手动接管不会因空闲自动失效，也不能由 CLI 的交还请求解除。

Agent 命令结束后，连续 60 秒没有新命令时，终端自动将控制权交给人类。后台服务是否继续运行不影响计时。自动交还后，下一次 CLI 执行可在终端空闲且没有未提交输入时重新接管。Agent 也可以在命令结束后主动交还：

```sh
./output/whos-typing-cli --session "$SESSION_ID" session release
./output/whos-typing-cli --session "$SESSION_ID" session status
```

`session status` 输出 `agent`、`human` 或 `human_takeover`，分别表示 Agent 控制、人类可自动让出控制权、以及人类手动接管。`session status` 和 `session read` 不获取控制权，也不重置空闲计时。远端命令超时或断开后，如果命令是否结束无法确认，程序不会据此自动交还；需要时可手动接管。

## 读取会话内容

启动 GUI 后，在另一个终端执行：

```sh
./output/whos-typing-cli --session "$SESSION_ID" session read
```

命令返回绑定会话的终端屏幕和已保留滚动历史的文本。结果会移除开头和末尾的换行符，最多返回末尾 10000 个 Unicode 字符。中间的换行和空格计入字符数；不足 10000 个字符时返回已有内容。返回的是终端文本快照，不包含颜色等显示样式，也不保证覆盖整个会话历史。

末尾连续多行完全相同时，如果合并后的表达更短，会将它们合并为 `内容 (重复 N 行)`，其中 `N` 为原有的总行数。压缩发生在字符截取之前，其他位置的重复内容不受影响。

全屏程序使用替代屏幕时，快照只能读取程序绘制到当前屏幕的内容；程序没有输出到终端的屏外内容无法读取。可以在 GUI 中翻页或放大窗口，再读取当前画面。

读取不会向终端发送输入，命令运行期间或进入远端会话后也可以使用，无需先接入远端 Bash。

## 接入远端 Bash

GUI 通过 SSH 等方式进入远端交互式 Bash 后，可以手动接入当前会话。接入前，确认 GUI 显示的是目标服务器的 Bash 提示符，且没有尚未提交的输入。不要在堡垒机菜单、密码提示或其他交互程序中执行接入命令。

1. 在另一个终端运行 `./output/whos-typing-cli --session "$SESSION_ID" attach --remote --shell bash --auto`，保持该命令运行。
2. 在确认框核对会话 ID 和标签页 ID。必要时点击「查看目标」，确认目标服务器的远端 Bash 已停在空提示符，然后点击「确认接入」。窗口会自动输入一次性接入命令。
3. 等待 CLI 显示「远端 Bash 已接入」。随后可在另一个终端运行 `./output/whos-typing-cli --session "$SESSION_ID" 'pwd'` 等命令，命令在已接入的远端 Shell 中执行。

自动接入等待确认期间，如果终端收到新输出或人工输入，接入会停止，需重新确认当前提示符。取消或连接中断后，若接入命令可能已发送，应先检查远端 Shell 状态。

也可以运行 `./output/whos-typing-cli --session "$SESSION_ID" attach --remote --shell bash` 使用手动方式。CLI 会输出一次性接入命令，由用户将完整命令粘贴到远端 Bash 空提示符并执行。

每个远端会话需要接入一次。接入命令会在当前远端 Shell 中设置提示符钩子，不修改 Shell 配置文件。当前远端接入仅支持 Bash；需要交互输入的程序仍应在 GUI 中操作。

远端会话退出后可以继续使用本地 CLI。如果经过堡垒机菜单返回本地 Shell 后 CLI 仍报告终端未就绪，确认 GUI 已显示本地 Shell 的空提示符，再按一次回车，然后重新执行 CLI 命令。

发送命令前，确认 GUI 仍处于预期的远端 Shell。程序会对可观察到的断线或菜单输出保守处理，但 SSH 连接发生变化且终端尚无输出时，无法仅凭终端流可靠识别目标；此时命令可能被随后出现的前台程序接收。

远端提示符出现后，如果后台程序继续输出，或分屏调整终端尺寸导致提示符重绘，CLI 可能报告远端状态未知。确认 GUI 仍显示目标服务器的空提示符；如果 Agent 仍持有控制权，先点击「接管」，或在允许时使用 `session release` 交还控制权。然后按一次回车，再执行 CLI 命令；手动接管后需先点击「交还 Agent」。也可以重新接入。

可以使用 `./output/whos-typing-cli --session "$SESSION_ID" --timeout 30s '命令'` 限制等待时间。CLI 超时或断开时，命令可能仍在远端运行，结果未知。不要直接重试；先在 GUI 确认命令状态，必要时按 Ctrl+C。

## 检查

```sh
GOWORK=off go list -m -json github.com/egoist/mygo
CGO_ENABLED=0 GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test ./...
openspec validate add-multi-session --strict
openspec validate --specs --strict
```

依赖信息中的 `Path` 应为 `github.com/egoist/mygo`，`Version` 应为 `v0.7.4`，不得出现 `Replace`。代码提交前使用 `gofmt` 格式化修改的 Go 文件。

## 交互验收

在真实窗口中按顺序检查：

1. 启动后直接输入 `echo mvpx`，按退格删除 `x`，再按回车。预期输出 `mvp`，并能继续输入。
2. 输入 `printf 'begin\n'; sleep 2; printf 'end\n'`。预期先看到 `begin`，命令结束时再看到 `end`。
3. 执行 `cd /tmp` 与 `pwd`。预期目录保持为系统实际解析的临时目录。
4. 执行 `export WT_MVP=ok` 与 `echo "$WT_MVP"`。预期输出 `ok`。
5. 执行 `sleep 60`，按 Ctrl+C。预期等待被中断，随后能执行 `echo resumed`。
6. 在仅有一个会话时执行 `exit`。预期窗口关闭且应用进程结束。
7. 重新启动，记录 Shell PID，再关闭窗口。fish 使用 `echo $fish_pid`，zsh/bash 使用 `echo $$`。预期应用及该 Shell 结束。

## 退出与启动失败

关闭窗口会结束所有终端会话。Shell 自行退出只关闭对应窗格，最后一个会话退出后应用结束；本版没有后台常驻或会话恢复。用户主动脱离会话的后台进程不在应用管理范围内。

若构建失败，查看终端输出。若原生库加载或 Shell 启动失败，应用会向标准错误输出原因并以非零状态退出；使用 `make run` 启动时，查看 `output/whos-typing.log` 以区分依赖下载、动态库加载和 Shell 路径错误。`make run` 返回成功只表示构建完成并发起了后台启动，不表示窗口已成功打开。失败验证应只修改测试进程的环境，不修改用户 Shell 配置。

## 验证记录

2026-10-30 在 macOS arm64、Go 1.27.3 下验证：

- 主会话独立核对 MyGo 版本和校验和，确认没有本地替换；格式、构建、vet 与单元测试通过。
- 通过 MyGo 自动化输入路径在真实窗口中验证退格编辑、执行命令、持续输出、工作目录和环境变量保留、Ctrl+C。主会话已回读验收截图。
- 执行 `exit` 后应用与默认 fish Shell 均退出。
- 通过测试进程的 `MYGO_GHOSTTY_VT` 和 `SHELL` 分别指向不存在的路径，验证失败原因输出和退出码 1。
- 单次启动确认窗口处于前台且终端节点聚焦；点击系统关闭按钮后应用和直接 Shell 进程均结束。
- 系统键盘注入路径存在待排查问题：System Events 输入的空格和回车未生效，官方 MyGo v0.7.3 终端示例也复现。独立 Cocoa 控件收到了对应按键事件，但当前输入法组合态仍可能影响 MyGo 的处理，尚不能确定根因。自动化文本输入路径的成功不能替代系统键盘验收，本版尚未完成全部交互验收。

本次检查将 Go 模块缓存和构建缓存放在 `/private/tmp` 下。GUI 验证使用上游自动化插件，只在 `e2e` 构建标签下启用；正常构建不包含该测试入口。

同日新增 CLI 后，独立执行 `make build`、`go vet ./...`、`go test ./...` 和 `go test -race ./internal/shell ./internal/control`，均通过。真实 fish 窗口验证了命令与输出显示、退出状态、二进制输出、未提交输入保护、前台 Ctrl+C，以及窗口关闭时 CLI 的错误返回。双向验证中，GUI 切换目录后 CLI 能读取新目录，CLI 设置变量后 GUI 能读取该变量。zsh 和 bash 的窗口验证覆盖了输出、退出状态和 Shell 退出。

Ctrl+C 的早期自动化测试曾有两次失败，当时没有记录发送按键时的焦点。后续在确认窗口和终端焦点后复验通过；窗口处于后台时，自动化工具可能返回成功但不会将按键送到终端，不能据此判断实际键盘行为。

```sh
CGO_ENABLED=0 GOWORK=off go build -tags e2e -o /tmp/whos-typing-e2e ./cmd/whos-typing
MYGO_AUTOMATION=/tmp/whos-typing-e2e.sock /tmp/whos-typing-e2e
```

自动化协议见上游 MyGo 的 `docs/plugins/automation.md`，可读取窗口、发送键盘事件和捕获窗口画面。本次原生库取自官方发行文件并核验 SHA-256，使用 `MYGO_GHOSTTY_VT` 指向临时文件，不修改用户缓存。

2026-11-02 在 macOS arm64、Go 1.27.3 下验证 Agent 控制权：

- 在临时 HOME、独立控制 socket 和测试窗口中，验证 CLI 获取控制权、人工接管正在等待输入的命令、命令继续接收人工输入、人工操作后交还、空闲 60 秒自动交还、再次获取控制权及主动交还。自动化路径不能代替物理键盘验收。
- 在深色终端检查粒子效果、文字和位于底部的提示符；确认框通过本地嵌套 Bash 触发并取消，未连接真实远端。界面测试另行验证确认框位于 Agent 效果上方。
- 760 × 448 DIP 测试窗口的动画稳定运行后，`ps` 两次单点 CPU 样本分别为 3.9% 和 5.3%；这是当前机器上的观察值。软件渲染时使用较少粒子和 15 帧每秒的绘制路径。
- 全量测试、竞争检查、`go vet`、构建和 OpenSpec 严格校验通过。测试使用已校验的上游原生库；fish、zsh、bash 和滚动视图相关测试均实际运行。

同日检查顶部悬浮胶囊时，使用临时 HOME、独立 zsh 和专用 socket 启动测试窗口。浅色和深色终端中均核对了胶囊位置、接管、交还、失败提示及覆盖首行文字时的快照；终端区域没有因胶囊出现而缩小。全量测试、`internal/app` 竞争检查、`go vet` 和构建通过；设置上游原生库后，4 项原生终端测试及滚动视图测试均实际运行。

本次窗口检查使用 MyGo 自动化输入，未验证物理键盘或连接真实远端 SSH。60 秒自动交还的真实窗口验证见上次记录，本次未重新等待计时。

同日使用 MyGo v0.7.4 复核毛玻璃胶囊。隔离窗口覆盖浅色和深色终端、粒子背景、前台命令接管与输入、交还失败提示、重新交还及主动释放；2 倍分辨率预览还检查了深浅系统主题和文字垂直位置。交互检查要求测试窗口处于前台，未操作已有会话。

在同一台 macOS arm64 机器上，分别运行旧样式和毛玻璃样式，动画稳定后的 5 次 `ps` CPU 样本中位数为 4.7% 和 6.5%。这是短时观察，不是性能基准；未测量 GPU。全量测试、`internal/app` 竞争检查、`go vet`、构建和 OpenSpec 严格校验通过；测试使用校验过的上游原生库，原生终端与滚动视图测试实际运行。本次未验证物理键盘或真实 SSH，也未重新等待 60 秒自动交还。

2026-11-03 在 macOS arm64、Go 1.27.3 下验证多会话。测试使用临时用户目录、独立 Shell 和仅供 `e2e` 构建使用的独立控制 socket，没有操作已有窗口或 SSH 会话。

- 使用已校验的上游 libghostty-vt 执行全量测试、`internal/app`、`internal/control`、`internal/shell` 的竞态检查和 `go vet`。fish、zsh、bash、原生终端及滚动视图测试实际运行。
- 在隔离窗口验证标签编号、快捷键创建、嵌套四窗格、背景创建、焦点保持、标签重命名和 Shell 标题隔离。分别检查四个窗格的命令结果及 Agent 控制胶囊，并在真实窗口拖动分隔线；UI 测试核对拖动后的终端尺寸、窄窗格接管和右键关闭。
- 两个会话分别经过 60 秒空闲交还；其中一个位于隐藏标签页。另验证人工接管不改变其他会话、隐藏标签页持续输出、并发命令输出隔离及超时后的结果未知。远端接入使用本地嵌套 Bash 模拟，分屏后确认原会话保留连接，新窗格使用独立本地 Shell；没有连接真实 SSH。
- MyGo 自动化路径验证键盘输入、中文文本提交、滚动、未提交输入保护和关闭标签页后的 Shell 回收。独立 UI 测试使用模拟剪贴板和输入法组合事件验证粘贴及跨会话组合输入；没有验证物理键盘、系统剪贴板或系统输入法的实际组合过程。
- 单会话空闲时的 4 次 CPU 样本为 2.1%、0.1%、3.1%、2.1%；四窗格加隐藏标签页持续输出时为 3.6%、4.1%、5.5%、5.8%。这是本机短时观察，不能作为性能基准；未单独统计重绘次数。

Shell 启动期间，CLI 可能在空提示符准备好前返回「终端尚未就绪」。测试中曾有两次 zsh 会话在等待 8 秒后仍返回该错误，原因未确认；随后独立 Shell 重复 5 次、隔离 GUI 连续启动 19 次均未复现，不能据此认定问题已经解决。
