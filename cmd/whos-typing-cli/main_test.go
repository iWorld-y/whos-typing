// main_test.go 测试 CLI 的本地选项。
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLocalOptions(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		handled bool
		want    []string
		exact   string
	}{
		{
			name:    "help",
			args:    []string{"--help"},
			handled: true,
			want:    []string{"用法:", "--timeout", "attach --remote", "session", "--help", "--version"},
		},
		{name: "version", args: []string{"--version"}, handled: true, exact: "whos-typing-cli 0.1.0\n"},
		{name: "no arguments"},
		{name: "shell command", args: []string{"pwd"}},
		{name: "timeout command", args: []string{"--timeout", "5s", "--help"}},
		{name: "extra argument", args: []string{"--version", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if got := handleLocalOption(tt.args, &output); got != tt.handled {
				t.Fatalf("处理状态 = %t，期望 %t", got, tt.handled)
			}
			if len(tt.want) == 0 && output.String() != tt.exact {
				t.Fatalf("输出 = %q，期望 %q", output.String(), tt.exact)
			}
			for _, want := range tt.want {
				if !strings.Contains(output.String(), want) {
					t.Errorf("输出 %q 不包含 %q", output.String(), want)
				}
			}
		})
	}
}

func TestParseWorkspaceArguments(t *testing.T) {
	for _, tc := range []struct {
		args                                []string
		action, session, command, direction string
		focus, json                         bool
		invalid                             bool
	}{
		{args: []string{"--session", "abc", "--timeout", "30s", "printf '%s' --x"}, action: "exec", session: "abc", command: "printf '%s' --x"},
		{args: []string{"--session", "abc", "--", "--help"}, action: "exec", session: "abc", command: "--help"},
		{args: []string{"session", "list", "--json"}, action: "session.list", json: true},
		{args: []string{"session", "current", "--json"}, action: "session.current", json: true},
		{args: []string{"tab", "new", "--json"}, action: "tab.new", json: true},
		{args: []string{"--session", "abc", "session", "split", "--direction", "down", "--focus", "--json"}, action: "session.split", session: "abc", direction: "down", focus: true, json: true},
		{args: []string{"--session", "abc", "attach", "--remote", "--shell", "bash", "--auto"}, action: "attach", session: "abc"},
		{args: []string{"session", "split", "--direction", "left"}, invalid: true},
		{args: []string{"--timeout", "0s", "pwd"}, invalid: true},
		{args: []string{"--session", "a", "--session", "b", "pwd"}, invalid: true},
		{args: []string{"--session", "a", "session", "list"}, invalid: true},
		{args: []string{"--", "one", "two"}, invalid: true},
		{args: []string{"--unknown"}, invalid: true},
	} {
		o, err := parseArgs(tc.args)
		if tc.invalid {
			if err == nil {
				t.Errorf("%v 未拒绝", tc.args)
			}
			continue
		}
		if err != nil || o.action != tc.action || o.session != tc.session || o.command != tc.command || o.direction != tc.direction || o.focus != tc.focus || o.json != tc.json {
			t.Errorf("%v: %+v %v", tc.args, o, err)
		}
	}
}

func TestParseRenameTab(t *testing.T) {
	o, err := parseArgs([]string{"tab", "rename", "--tab", "tab-id", "工作 会话"})
	if err != nil || o.action != "tab.rename" || o.tab != "tab-id" || o.title != "工作 会话" {
		t.Fatalf("解析错误: %+v %v", o, err)
	}
	for _, args := range [][]string{{"tab", "rename", "--tab", "tab-id", " "}, {"tab", "rename", "--tab", "", "name"}, {"tab", "rename", "--tab", "id"}, {"--session", "a", "tab", "rename", "--tab", "id", "name"}} {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("未拒绝参数 %v", args)
		}
	}
}
