// register.go 在需要权限时协调 CLI 安装授权。
package cliinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// ErrDeclined 表示用户选择暂不安装 CLI。
var ErrDeclined = errors.New("用户未授权安装 CLI")

// Register 创建 CLI 链接，只有权限不足时才请求用户授权。
func Register(ctx context.Context, source, target string, confirm func() (bool, error), elevate func(context.Context, string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := Link(source, target)
	if err == nil || !errors.Is(err, os.ErrPermission) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	approved, err := confirm()
	if err != nil {
		return fmt.Errorf("确认安装 CLI 失败: %w", err)
	}
	if !approved {
		return ErrDeclined
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return elevate(ctx, source)
}
