// whos-typing-cli 将命令发送到运行中的终端窗口。
package main

import "os"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
