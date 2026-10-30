// whos-typing-cli 将命令发送到运行中的终端窗口。
package main

import (
	"fmt"
	"os"
	"time"

	"whos-typing/internal/control"
)

func main() {
	if len(os.Args) == 5 && os.Args[1] == "attach" && os.Args[2] == "--remote" && os.Args[3] == "--shell" {
		if err := control.AttachRemote(os.Args[4], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "--timeout" {
		duration, err := time.ParseDuration(os.Args[2])
		if err != nil || duration <= 0 {
			fmt.Fprintln(os.Stderr, "超时时间必须是大于 0 的时间值，例如 30s")
			os.Exit(2)
		}
		code, err := control.RunWithTimeout(os.Args[3], duration, os.Stdout)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(code)
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "用法: whos-typing-cli [--timeout 时间] '命令'\n      whos-typing-cli attach --remote --shell bash")
		os.Exit(2)
	}
	code, err := control.Run(os.Args[1], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}
