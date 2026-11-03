// 本文件生成标签默认标题并转义控制字符。
package app

import (
	"strconv"
	"strings"
	"unicode"

	"whos-typing/internal/shell"
)

func titleField(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) {
			quoted := strconv.QuoteRune(r)
			out.WriteString(quoted[1 : len(quoted)-1])
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func contextTitle(value shell.TitleContext) string {
	title := titleField(value.User) + "@" + titleField(value.Host) + ":" + titleField(value.Directory)
	if value.Unknown {
		title += "（状态未知）"
	}
	return title
}

func (w *workspace) tabTitleLocked(tab *workspaceTab) string {
	if tab.title != "" {
		return tab.title
	}
	if p := w.panes[tab.last]; p != nil {
		if provider, ok := p.target.(interface{ TitleContext() shell.TitleContext }); ok {
			return contextTitle(provider.TitleContext())
		}
	}
	return contextTitle(shell.InitialTitleContext())
}
