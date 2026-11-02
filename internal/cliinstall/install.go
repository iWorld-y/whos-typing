// Package cliinstall 定位应用内的 CLI 和 Skill 并安全创建软链接。
package cliinstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const Name = "whos-typing-cli"

var (
	ErrTemporaryApp = errors.New("应用位于临时位置")
	ErrConflict     = errors.New("已有同名命令")
)

// Source 返回应用资源中的可执行 CLI 路径。
func Source(resources string) (string, error) {
	resources, err := filepath.Abs(resources)
	if err != nil {
		return "", fmt.Errorf("解析应用资源路径失败: %w", err)
	}
	if resources == "/Volumes" || strings.HasPrefix(resources, "/Volumes/") || strings.Contains(resources, "/AppTranslocation/") {
		return "", ErrTemporaryApp
	}

	source := filepath.Join(resources, "bin", Name)
	if err = checkSource(source); err != nil {
		return "", err
	}
	return source, nil
}

// Link 创建指向 CLI 的软链接，不替换已有文件或其他链接。
func Link(source, target string) error {
	err := checkSource(source)
	if err != nil {
		return err
	}

	if err = os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("创建命令目录失败: %w", err)
	}

	if err = os.Symlink(source, target); err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("创建 CLI 软链接失败: %w", err)
	}

	existing, err := os.Readlink(target)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrConflict, target)
	}
	if !filepath.IsAbs(existing) {
		existing = filepath.Join(filepath.Dir(target), existing)
	}
	if filepath.Clean(existing) != filepath.Clean(source) {
		return fmt.Errorf("%w: %s", ErrConflict, target)
	}

	return nil
}

func checkSource(source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("检查应用内的 CLI 失败: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("应用内的 CLI 不可执行: %s", source)
	}
	return nil
}
