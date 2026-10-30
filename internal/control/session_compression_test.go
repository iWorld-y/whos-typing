package control

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionReadCompressesOnlyRepeatedTail(t *testing.T) {
	line := "重复内容😀保留普通空格 1234567890"
	for _, tc := range []struct {
		name, text, want string
	}{
		{"one_line", "前文\n" + line, "前文\n" + line},
		{"short_two_lines", "前文\nx\nx", "前文\nx\nx"},
		{"two_lines", "前文\n" + line + "\n" + line, "前文\n" + line + " (重复 2 行)"},
		{"repeated_not_at_end", line + "\n" + line + "\n不同内容", line + "\n" + line + "\n不同内容"},
		{"spaces_are_significant", line + "\n" + line + " \n" + line + " ", line + "\n" + line + "  (重复 2 行)"},
		{"crlf", "\r\n  前文 \r\n" + line + "\r\n" + line + "\r\n" + line + "\r\n", "  前文 \r\n" + line + " (重复 3 行)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := readTestServer(t, readableExecutor{read: func() (string, error) { return tc.text, nil }})
			var output bytes.Buffer
			if err := readSession(path, &output); err != nil {
				t.Fatal(err)
			}
			if output.String() != tc.want {
				t.Fatalf("输出 = %q，期望 %q", output.String(), tc.want)
			}
		})
	}
}

func TestSessionReadCompressesBeforeLimit(t *testing.T) {
	line := "0123456789012345678901234567890"
	prefix := "EARLY_CONTEXT\n" + strings.Repeat("旧", 9800) + "\n"
	input := prefix + strings.Repeat(line+"\n", 22) + line
	want := prefix + line + " (重复 23 行)"
	if utf8.RuneCountInString(input) <= 10000 || utf8.RuneCountInString(want) >= 10000 {
		t.Fatal("测试数据未覆盖压缩前超过上限、压缩后低于上限")
	}
	path := readTestServer(t, readableExecutor{read: func() (string, error) { return input, nil }})
	var output bytes.Buffer
	if err := readSession(path, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != want {
		t.Fatalf("压缩未保留较早内容: 字符 %d，期望 %d", utf8.RuneCount(output.Bytes()), utf8.RuneCountInString(want))
	}
}

func TestSessionReadCompressedLimit(t *testing.T) {
	line := "中文😀尾行 12345678901234567890"
	compressed := line + " (重复 3 行)"
	prefixLength := 10000 - utf8.RuneCountInString(compressed) - 1
	for _, extra := range []int{0, 17} {
		prefix := strings.Repeat("前", prefixLength+extra)
		input := prefix + "\n" + strings.Repeat(line+"\n", 2) + line
		want := strings.Repeat("前", prefixLength) + "\n" + compressed
		path := readTestServer(t, readableExecutor{read: func() (string, error) { return input, nil }})
		var output bytes.Buffer
		if err := readSession(path, &output); err != nil {
			t.Fatal(err)
		}
		if output.String() != want || !utf8.Valid(output.Bytes()) || utf8.RuneCount(output.Bytes()) != 10000 {
			t.Fatalf("压缩后边界错误: extra=%d 字符=%d", extra, utf8.RuneCount(output.Bytes()))
		}
	}
}
