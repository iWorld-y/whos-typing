// info.go 提供无需连接终端的本地选项。
package main

import (
	"fmt"
	"io"

	"whos-typing"
)

const usage = `用法: whos-typing-cli [--session ID] [--timeout 时间] [--] '命令'
      whos-typing-cli [--session ID] attach --remote --shell bash [--auto]
      whos-typing-cli [--session ID] session {read|status|release|focus}
      whos-typing-cli session {list|current} [--json]
      whos-typing-cli tab new [--focus] [--json]
      whos-typing-cli tab rename --tab ID '标题'
      whos-typing-cli [--session ID] session split --direction {right|down} [--focus] [--json]
      whos-typing-cli ssh [--config 文件] {connect|status|disconnect} TARGET
      whos-typing-cli ssh [--config 文件] run TARGET '命令'
      whos-typing-cli --help
      whos-typing-cli --version

多个会话时，执行、读取、控制、接入和分屏必须指定 --session。
新窗口的完整 ID 如 session_01、tab_01，请原样使用；旧窗口的 ID 格式可能不同。
要操作“这个会话”，先查询 session current --json，再固定返回的 ID。`

// handleLocalOption 处理无需连接终端的本地选项。
func handleLocalOption(args []string, out io.Writer) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "--help":
		fmt.Fprintln(out, usage)
	case "--version":
		fmt.Fprintln(out, "whos-typing-cli", whostyping.Version())
	default:
		return false
	}
	return true
}
