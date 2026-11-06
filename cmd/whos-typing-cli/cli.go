// 本文件执行 CLI 操作并输出结果。
package main

import (
	"encoding/json"
	"fmt"
	"io"

	"whos-typing/internal/control"
)

func printJSON(out io.Writer, value any) error { return json.NewEncoder(out).Encode(value) }

func run(args []string, out, errOut io.Writer) int {
	if len(args) > 0 && args[0] == "ssh" {
		return runSSH(args[1:], out, errOut)
	}
	if handleLocalOption(args, out) {
		return 0
	}
	o, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	code := 0
	switch o.action {
	case "exec":
		code, err = control.RunSession(o.session, o.command, o.timeout, out)
	case "session.read":
		err = control.ReadSessionFor(o.session, out)
	case "session.status", "session.release":
		err = control.ControlSession(o.session, o.action, out)
		if err == nil && o.action == "session.release" {
			_, err = fmt.Fprintln(out, "已交还终端控制权。")
		}
	case "attach":
		err = control.AttachSession(o.session, o.shell, o.auto, out)
	case "session.list":
		var list control.SessionList
		list, err = control.ListWorkspace()
		if err == nil {
			if o.json {
				err = printJSON(out, list)
			} else {
				for _, s := range list.Sessions {
					if _, err = fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", s.SessionID, s.TabID, s.TabTitle, s.Title, s.Control); err != nil {
						break
					}
				}
			}
		}
	case "session.current":
		var current control.CurrentSession
		current, err = control.CurrentWorkspace()
		if err == nil {
			if o.json {
				err = printJSON(out, current)
			} else {
				_, err = fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\twindow_focused=%t\n", current.SessionID, current.TabID, current.TabTitle, current.Title, current.Control, current.WindowFocused)
			}
		}
	case "tab.new", "session.split":
		var created control.CreatedSession
		if o.action == "tab.new" {
			created, err = control.CreateTab(o.focus)
		} else {
			created, err = control.SplitSession(o.session, o.direction, o.focus)
		}
		if err == nil {
			if o.json {
				err = printJSON(out, created)
			} else {
				_, err = fmt.Fprintf(out, "%s\t%s\n", created.SessionID, created.TabID)
			}
		}
	case "tab.rename":
		err = control.RenameTab(o.tab, o.title)
	case "session.focus":
		err = control.FocusSession(o.session)
	}
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	return code
}
