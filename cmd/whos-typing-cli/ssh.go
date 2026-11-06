package main

import (
	"encoding/json"
	"fmt"
	"io"
	"whos-typing/internal/sshlink"
)

func runSSH(args []string, out, errOut io.Writer) int {
	config := ""
	if len(args) > 0 && args[0] == "--config" {
		if len(args) < 3 || args[1] == "" {
			fmt.Fprintln(errOut, "--config 需要 SSH 配置文件路径")
			return 2
		}
		config, args = args[1], args[2:]
	}
	if len(args) < 2 || (args[0] == "run" && len(args) != 3) || (args[0] != "run" && len(args) != 2) {
		fmt.Fprintln(errOut, "用法: ssh {connect|status|disconnect} TARGET 或 ssh run TARGET '命令'")
		return 2
	}
	switch args[0] {
	case "connect", "status", "disconnect", "run":
	default:
		fmt.Fprintln(errOut, "未知 SSH 操作")
		return 2
	}
	link, err := sshlink.New(args[1], config)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	code := 0
	switch args[0] {
	case "connect":
		err = link.Connect()
	case "disconnect":
		err = link.Disconnect()
	case "run":
		code, err = link.Run(args[2], out, errOut)
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		if code == 0 {
			code = 1
		}
		return code
	}
	if args[0] != "run" {
		connected := link.Connected()
		err = json.NewEncoder(out).Encode(map[string]any{"target": link.Target, "connected": connected, "socket": link.Socket, "scope": "ssh_transport", "persistent_shell": false})
		if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		if args[0] == "status" && !connected {
			return 1
		}
	}
	return code
}
