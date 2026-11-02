// cli_install.go 在应用启动后注册随包提供的 CLI。
package app

import (
	"context"
	"errors"
	"os"
	"runtime"

	"github.com/egoist/mygo"

	"whos-typing/internal/cliinstall"
)

// installBundledCLI 尝试安装命令，并在需要时请求用户授权。
func installBundledCLI(ctx context.Context, win *mygo.Window) {
	if runtime.GOOS != "darwin" || !mygo.App.IsPackaged() {
		return
	}
	resources, err := mygo.App.Path(mygo.PathResources)
	if err != nil {
		showCLIMessage(ctx, win, "无法安装命令行工具", err.Error())
		return
	}
	source, err := cliinstall.Source(resources)
	if errors.Is(err, cliinstall.ErrTemporaryApp) {
		showCLIMessage(ctx, win, "请先安装应用", "请将 Who's Typing 拖入应用程序文件夹，再重新打开以安装命令行工具。")
		return
	}
	if err != nil {
		showCLIMessage(ctx, win, "无法安装命令行工具", err.Error())
		return
	}
	err = cliinstall.Register(ctx, source, cliinstall.Target, func() (bool, error) {
		result, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent:        win,
			Type:          mygo.MessageQuestion,
			Title:         "安装命令行工具",
			Message:       "是否安装 Who's Typing CLI？",
			Detail:        "将在 /usr/local/bin 创建 CLI 软链接，并将 Skill 安装到 ~/.agents/skills。创建 CLI 软链接需要管理员授权。",
			Buttons:       []string{"暂不安装", "安装"},
			DefaultButton: 1,
			CancelButton:  0,
		})
		return result.Button == 1, err
	}, cliinstall.LinkAuthorized)
	if errors.Is(err, cliinstall.ErrDeclined) || errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(err, cliinstall.ErrConflict) {
		showCLIMessage(ctx, win, "命令行工具未安装", "已有同名命令，应用未覆盖它。请检查 /usr/local/bin/whos-typing-cli。")
		return
	}
	if err != nil {
		showCLIMessage(ctx, win, "命令行工具未安装", err.Error())
		return
	}
	if ctx.Err() != nil {
		return
	}
	userDir, err := os.UserHomeDir()
	if err == nil {
		err = cliinstall.InstallSkill(resources, userDir)
	}
	if errors.Is(err, cliinstall.ErrSkillConflict) {
		showCLIMessage(ctx, win, "Skill 未安装", "已有同名 Skill，应用未覆盖它。请检查 ~/.agents/skills/whos-typing-cli。")
		return
	}
	if err != nil {
		showCLIMessage(ctx, win, "Skill 未安装", err.Error())
	}
}

// showCLIMessage 显示安装提示，不影响主窗口运行。
func showCLIMessage(ctx context.Context, win *mygo.Window, title, message string) {
	if ctx.Err() != nil {
		return
	}
	_, _ = mygo.Dialog.Message(mygo.MessageOptions{
		Parent:  win,
		Type:    mygo.MessageInfo,
		Title:   title,
		Message: message,
	})
}
