// whos-typing-cli 将命令发送到运行中的终端窗口。
package main

import "os"

func main() { os.Exit(runWithInput(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
