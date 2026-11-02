// skill_test.go 验证应用内 Skill 的安装和冲突处理。
package cliinstall

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testSkillResources(t *testing.T) (string, string) {
	t.Helper()
	resources := testResources(t)
	source := filepath.Join(resources, "skills", Name)
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"SKILL.md":          "skill content",
		"WHOS_TYPING_SKILL": Name + "\n",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return resources, source
}

func TestInstallSkillLinksBundledSkill(t *testing.T) {
	resources, source := testSkillResources(t)
	userDir := t.TempDir()
	target := filepath.Join(userDir, ".agents", "skills", Name)
	if err := InstallSkill(resources, userDir); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(target); err != nil || got != source {
		t.Fatalf("Skill 软链接 = %q，错误 = %v，期望 %q", got, err, source)
	}
	if err := InstallSkill(resources, userDir); err != nil {
		t.Fatalf("重复安装失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("updated"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil || string(got) != "updated" {
		t.Fatalf("Skill 内容 = %q，错误 = %v", got, err)
	}
}

func TestInstallSkillKeepsExistingMarkedDirectory(t *testing.T) {
	resources, _ := testSkillResources(t)
	userDir := t.TempDir()
	target := filepath.Join(userDir, ".agents", "skills", Name)
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "WHOS_TYPING_SKILL"), []byte(Name+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := InstallSkill(resources, userDir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil || string(got) != "existing" {
		t.Fatalf("已有 Skill 被修改：内容 = %q，错误 = %v", got, err)
	}
}

func TestInstallSkillPreservesOtherTargets(t *testing.T) {
	resources, _ := testSkillResources(t)
	for _, kind := range []string{"file", "directory", "foreign-marker", "symlink", "dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			userDir := t.TempDir()
			target := filepath.Join(userDir, ".agents", "skills", Name)
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "file":
				err = os.WriteFile(target, []byte("existing"), 0644)
			case "directory", "foreign-marker":
				err = os.Mkdir(target, 0755)
				if err == nil && kind == "foreign-marker" {
					err = os.WriteFile(filepath.Join(target, "WHOS_TYPING_SKILL"), []byte("other"), 0644)
				}
			case "symlink":
				err = os.Symlink(t.TempDir(), target)
			case "dangling-symlink":
				err = os.Symlink("missing", target)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := InstallSkill(resources, userDir); !errors.Is(err, ErrSkillConflict) {
				t.Fatalf("Skill 冲突错误 = %v", err)
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("已有 Skill 发生变化: %v", err)
			}
		})
	}
}

func TestInstallSkillRejectsInvalidBundledSkill(t *testing.T) {
	for _, invalid := range []string{"missing-skill", "missing-marker", "foreign-marker"} {
		t.Run(invalid, func(t *testing.T) {
			resources, source := testSkillResources(t)
			switch invalid {
			case "missing-skill":
				if err := os.Remove(filepath.Join(source, "SKILL.md")); err != nil {
					t.Fatal(err)
				}
			case "missing-marker":
				if err := os.Remove(filepath.Join(source, "WHOS_TYPING_SKILL")); err != nil {
					t.Fatal(err)
				}
			case "foreign-marker":
				if err := os.WriteFile(filepath.Join(source, "WHOS_TYPING_SKILL"), []byte("other"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			userDir := t.TempDir()
			if err := InstallSkill(resources, userDir); err == nil {
				t.Fatal("不完整的 Skill 不应安装")
			}
			target := filepath.Join(userDir, ".agents", "skills", Name)
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("目标不应存在: %v", err)
			}
		})
	}
}
