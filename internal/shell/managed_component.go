// 本文件携带远端执行组件，并构造经复用连接启动它的命令。
package shell

import (
	_ "embed"
	"encoding/base64"
)

//go:embed managed_component.py
var managedComponent string

// managedRemoteCommand 把组件编码为远端命令。登录 Shell 可能是 fish 等，
// 统一经 bash -c 执行；Base64 只含字母数字和 +/=，可安全放入单引号。
// sync 标记用于跳过登录 Shell 初始化混入 stdout 的文本。
func managedRemoteCommand(sync string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(managedComponent))
	return "bash -c 'python3 -c \"$(printf %s " + encoded + " | base64 --decode)\" " + sync + "'"
}
