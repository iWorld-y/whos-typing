# 独立服务器连接入口

基于 v0.3.0，为日常非交互服务器操作新增 CLI 子命令 ssh connect/status/run/disconnect。原共享终端继续保留用于人工认证和交互会话。

## 决策

- 使用系统 OpenSSH，无新依赖，不读取或复制密钥；用户通过 SSH 配置指定账户、端口及跳板机。
- socket 按用户隔离，目标与显式配置内容参与连接标识。
- connect 显式建连并验证 master 存活；run 只复用已存在 master。检查后 socket 消失仍由 ProxyCommand=false 阻止连接回退。
- 每个 run 为独立命令通道，不承诺目录/环境连续性。无需 GUI 或远端提示符注入。
- 不重放命令；255 明确为未知结果。status.connected 只证明本地复用进程，不代表远端任务成功。
- 长任务存活、命令 ID 和查询历史不在本轮实现。

## 验收

拒绝选项及 Shell 注入目标；保留远端退出码；socket 失效不直连、不本地执行；重复 connect 不重复建立 master；配置更改不误复用；原 GUI/CLI 测试继续通过。
