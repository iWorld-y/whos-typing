// skill.go 将应用附带的 Skill 注册到用户的 Agent 目录。
package cliinstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const skillMarker = "WHOS_TYPING_SKILL"

// ErrSkillConflict 表示已有无法安全替换的同名 Skill。
var ErrSkillConflict = errors.New("已有同名 Skill")

// InstallSkill 将应用内的 Skill 链接到用户目录，保留已有的同名 Skill。
func InstallSkill(resources, userDir string) error {
	if !filepath.IsAbs(userDir) {
		return fmt.Errorf("用户目录路径无效: %s", userDir)
	}
	source, err := filepath.Abs(filepath.Join(resources, "skills", Name))
	if err != nil {
		return fmt.Errorf("解析应用内 Skill 路径失败: %w", err)
	}
	if !markedSkill(source) {
		return errors.New("应用内 Skill 缺少有效的 SKILL.md 或来源标记")
	}

	target := filepath.Join(userDir, ".agents", "skills", Name)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return fmt.Errorf("创建 Skill 目录失败: %w", err)
	}
	err = os.Symlink(source, target)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("创建 Skill 软链接失败: %w", err)
	}

	if existing, err := os.Readlink(target); err == nil {
		if !filepath.IsAbs(existing) {
			existing = filepath.Join(filepath.Dir(target), existing)
		}
		if filepath.Clean(existing) == filepath.Clean(source) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrSkillConflict, target)
	}
	if info, err := os.Lstat(target); err == nil && info.IsDir() && markedSkill(target) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrSkillConflict, target)
}

// markedSkill 检查 Skill 文件和来源标记。
func markedSkill(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	marker, err := os.ReadFile(filepath.Join(dir, skillMarker))
	return err == nil && strings.TrimSpace(string(marker)) == Name
}
