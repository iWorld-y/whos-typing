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
