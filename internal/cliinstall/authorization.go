// authorization.go 在用户授权后安装 CLI 软链接。
package cliinstall

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Target 是 macOS 默认命令目录中的 CLI 路径。
const Target = "/usr/local/bin/" + Name

const authorizationScript = `on run argv
	do shell script (item 1 of argv) with administrator privileges
end run`

// LinkAuthorized 请求管理员授权，并创建 CLI 软链接。
func LinkAuthorized(ctx context.Context, source string) error {
	if err := checkSource(source); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", authorizationScript, linkCommand(source, Target))
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := strings.TrimSpace(string(output))
		if strings.HasSuffix(message, "(-128)") {
			return ErrDeclined
		}
		if message == "" {
			return fmt.Errorf("管理员授权安装失败: %w", err)
		}
		return fmt.Errorf("管理员授权安装失败: %s: %w", message, err)
	}
	existing, err := os.Readlink(Target)
	if err != nil {
		return fmt.Errorf("检查已安装的 CLI 失败: %w", err)
	}
	if filepath.Clean(existing) != filepath.Clean(source) {
		return fmt.Errorf("%w: %s", ErrConflict, Target)
	}
	return nil
}

// linkCommand 构造不覆盖已有目标的安装命令。
func linkCommand(source, target string) string {
	source = shellQuote(source)
	dir := shellQuote(filepath.Dir(target))
	target = shellQuote(target)
	return fmt.Sprintf("/bin/test -f %s && /bin/test -x %s && /bin/mkdir -p %s && if [ -e %s ] || [ -L %s ]; then exit 1; fi && /bin/ln -s -h %s %s", source, source, dir, target, target, source, target)
}

// shellQuote 将路径作为单个 Shell 参数引用。
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
