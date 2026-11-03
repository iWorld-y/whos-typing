// 本文件解析会话和命令选项。
package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type options struct {
	action, session, command, direction, shell, tab, title string
	timeout                                                time.Duration
	json, focus, auto                                      bool
}

// parseArgs 只解析顶层选项，不拆分用户提供的命令字符串。
func parseArgs(args []string) (options, error) {
	var o options
	var timeoutSet, sessionSet, separator bool
	for len(args) > 0 {
		switch args[0] {
		case "--":
			separator = true
			args = args[1:]
		case "--session", "--timeout":
			if len(args) < 2 || args[1] == "" {
				return o, errors.New("顶层选项缺少参数")
			}
			if args[0] == "--session" {
				if sessionSet {
					return o, errors.New("--session 不能重复")
				}
				o.session, sessionSet = args[1], true
			} else {
				if timeoutSet {
					return o, errors.New("--timeout 不能重复")
				}
				var err error
				o.timeout, err = time.ParseDuration(args[1])
				if err != nil || o.timeout <= 0 {
					return o, errors.New("超时时间必须是大于 0 的时间值，例如 30s")
				}
				timeoutSet = true
			}
			args = args[2:]
			continue
		}
		break
	}
	if len(args) == 0 {
		return o, errors.New(usage)
	}
	if !separator && strings.HasPrefix(args[0], "--") {
		return o, fmt.Errorf("未知选项: %s", args[0])
	}
	if separator || len(args) == 1 {
		if len(args) != 1 {
			return o, errors.New("命令必须作为一个完整参数传入")
		}
		o.action, o.command = "exec", args[0]
		return o, nil
	}
	if timeoutSet {
		return o, errors.New("--timeout 仅适用于执行命令")
	}
	if len(args) >= 2 && args[0] == "session" {
		o.action = "session." + args[1]
		args = args[2:]
		switch o.action {
		case "session.read", "session.status", "session.release", "session.focus":
			if len(args) != 0 {
				return o, errors.New(usage)
			}
		case "session.list", "session.current":
			if sessionSet {
				return o, errors.New("会话查询不接受 --session")
			}
			if len(args) == 1 && args[0] == "--json" {
				o.json = true
			} else if len(args) != 0 {
				return o, errors.New(usage)
			}
		case "session.split":
			for len(args) > 0 {
				switch args[0] {
				case "--direction":
					if len(args) < 2 || o.direction != "" {
						return o, errors.New("请指定一次分屏方向")
					}
					o.direction, args = args[1], args[2:]
				case "--focus":
					if o.focus {
						return o, errors.New("--focus 不能重复")
					}
					o.focus, args = true, args[1:]
				case "--json":
					if o.json {
						return o, errors.New("--json 不能重复")
					}
					o.json, args = true, args[1:]
				default:
					return o, errors.New(usage)
				}
			}
			if o.direction != "right" && o.direction != "down" {
				return o, errors.New("分屏方向必须是 right 或 down")
			}
		default:
			return o, errors.New(usage)
		}
		return o, nil
	}
	if len(args) >= 2 && args[0] == "tab" && args[1] == "rename" {
		if sessionSet {
			return o, errors.New("标签页重命名不接受 --session")
		}
		if len(args) != 5 || args[2] != "--tab" || args[3] == "" {
			return o, errors.New("用法: tab rename --tab ID '标题'")
		}
		if strings.TrimSpace(args[4]) == "" {
			return o, errors.New("标签页名称不能为空")
		}
		o.action, o.tab, o.title = "tab.rename", args[3], args[4]
		return o, nil
	}
	if len(args) >= 2 && args[0] == "tab" && args[1] == "new" {
		if sessionSet {
			return o, errors.New("新建标签页不接受 --session")
		}
		o.action = "tab.new"
		for _, arg := range args[2:] {
			switch arg {
			case "--focus":
				if o.focus {
					return o, errors.New("--focus 不能重复")
				}
				o.focus = true
			case "--json":
				if o.json {
					return o, errors.New("--json 不能重复")
				}
				o.json = true
			default:
				return o, errors.New(usage)
			}
		}
		return o, nil
	}
	if args[0] == "attach" {
		o.action = "attach"
		var remote bool
		args = args[1:]
		for len(args) > 0 {
			switch args[0] {
			case "--remote":
				if remote {
					return o, errors.New("--remote 不能重复")
				}
				remote, args = true, args[1:]
			case "--auto":
				if o.auto {
					return o, errors.New("--auto 不能重复")
				}
				o.auto, args = true, args[1:]
			case "--shell":
				if len(args) < 2 || o.shell != "" {
					return o, errors.New("请指定一次远端 Shell")
				}
				o.shell, args = args[1], args[2:]
			default:
				return o, errors.New(usage)
			}
		}
		if !remote || o.shell != "bash" {
			return o, errors.New("远端接入仅支持 --remote --shell bash")
		}
		return o, nil
	}
	return o, errors.New(usage)
}
