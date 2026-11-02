// info.go 提供无需连接终端的本地选项。
package main

import (
	"fmt"
	"io"
)

const version = "0.1.0"

const usage = `用法: whos-typing-cli [--timeout 时间] '命令'
      whos-typing-cli attach --remote --shell bash [--auto]
      whos-typing-cli session {read|status|release}
      whos-typing-cli --help
      whos-typing-cli --version`

// handleLocalOption 处理无需连接终端的本地选项。
func handleLocalOption(args []string, out io.Writer) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "--help":
		fmt.Fprintln(out, usage)
	case "--version":
		fmt.Fprintln(out, "whos-typing-cli", version)
	default:
		return false
	}
	return true
}
