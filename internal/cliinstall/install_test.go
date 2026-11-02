// install_test.go 测试 CLI 软链接的创建与冲突处理。
package cliinstall

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func testResources(t *testing.T) string {
	t.Helper()
	resources := filepath.Join(t.TempDir(), "Who's Typing.app", "Contents", "Resources")
	if err := os.MkdirAll(filepath.Join(resources, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "bin", Name), []byte("cli"), 0755); err != nil {
		t.Fatal(err)
	}
	return resources
}

func TestLinkCreatesAndKeepsExistingLink(t *testing.T) {
	resources := testResources(t)
	source, err := Source(resources)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "bin", Name)
	if err := Link(source, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(target)
	if err != nil || got != source {
		t.Fatalf("软链接 = %q，错误 = %v，期望 %q", got, err, source)
	}
	if err := Link(source, target); err != nil {
		t.Fatalf("重复安装失败: %v", err)
	}
}

func TestExistingLinkDoesNotRequireDirectoryWritePermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 用户不受目录写入权限限制")
	}
	source, err := Source(testResources(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, Name)
	if err := os.Symlink(source, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	if err := Link(source, target); err != nil {
		t.Fatalf("已有正确链接时不应请求写入权限: %v", err)
	}
}

func TestLinkDoesNotReplaceExistingFiles(t *testing.T) {
	resources := testResources(t)
	source, err := Source(resources)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), Name)
			switch kind {
			case "file":
				err = os.WriteFile(target, []byte("existing"), 0644)
			case "directory":
				err = os.Mkdir(target, 0755)
			case "symlink":
				err = os.Symlink("another-cli", target)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := Link(source, target); !errors.Is(err, ErrConflict) {
				t.Fatalf("冲突错误 = %v", err)
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("已有文件发生变化: %v", err)
			}
		})
	}
}

func TestSourceRejectsTemporaryAndMissingApps(t *testing.T) {
	for _, path := range []string{
		"/Volumes/Who's Typing/Who's Typing.app/Contents/Resources",
		"/private/var/folders/a/b/AppTranslocation/id/d/Who's Typing.app/Contents/Resources",
	} {
		if _, err := Source(path); !errors.Is(err, ErrTemporaryApp) {
			t.Errorf("临时路径 %q 的错误 = %v", path, err)
		}
	}
	if _, err := Source(t.TempDir()); err == nil {
		t.Fatal("缺少 CLI 时不应返回有效路径")
	}
}

func TestLinkRejectsMissingSource(t *testing.T) {
	target := filepath.Join(t.TempDir(), "bin", Name)
	if err := Link(filepath.Join(t.TempDir(), "missing"), target); err == nil {
		t.Fatal("CLI 不存在时不应创建软链接")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("目标不应存在: %v", err)
	}
}

func TestLinkReportsPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 用户不受目录写入权限限制")
	}
	source, err := Source(testResources(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	if err := Link(source, filepath.Join(dir, Name)); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("权限错误 = %v", err)
	}
}

func TestLinkReportsConflictBeforePermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 用户不受目录写入权限限制")
	}
	source, err := Source(testResources(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, Name)
	if err := os.WriteFile(target, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	if err := Link(source, target); !errors.Is(err, ErrConflict) {
		t.Fatalf("应先报告命令冲突，实际错误 = %v", err)
	}
}

func TestAuthorizedCommandQuotesPathsAndPreservesConflicts(t *testing.T) {
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "Who's Typing;$(touch INJECTED)")
	if err := os.Mkdir(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, Name)
	if err := os.WriteFile(source, []byte("cli"), 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "bin", Name)
	cmd := exec.Command("/bin/sh", "-c", linkCommand(source, target))
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("创建链接失败: %v: %s", err, output)
	}
	if got, err := os.Readlink(target); err != nil || got != source {
		t.Fatalf("软链接 = %q，错误 = %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "INJECTED")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("路径被当作命令执行: %v", err)
	}
	cmd = exec.Command("/bin/sh", "-c", linkCommand(source, target))
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Fatal("目标已存在时不应再次创建链接")
	}
	if got, err := os.Readlink(target); err != nil || got != source {
		t.Fatalf("原有软链接发生变化: %q，%v", got, err)
	}
}

func TestAuthorizedCommandDoesNotReplaceExistingTargets(t *testing.T) {
	source, err := Source(testResources(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"file", "directory", "dangling symlink"} {
		t.Run(kind, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), Name)
			var err error
			switch kind {
			case "file":
				err = os.WriteFile(target, []byte("existing"), 0644)
			case "directory":
				err = os.Mkdir(target, 0755)
			case "dangling symlink":
				err = os.Symlink("missing", target)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := exec.Command("/bin/sh", "-c", linkCommand(source, target)).Run(); err == nil {
				t.Fatal("目标已存在时不应创建链接")
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("已有目标发生变化: %v", err)
			}
		})
	}
}

func TestAuthorizedCommandRejectsMissingSource(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "bin", Name)
	if err := exec.Command("/bin/sh", "-c", linkCommand(filepath.Join(dir, "missing"), target)).Run(); err == nil {
		t.Fatal("CLI 不存在时不应执行安装")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("目标不应存在: %v", err)
	}
}

func TestRegisterRequestsAuthorizationAfterPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 用户不受目录写入权限限制")
	}
	source, err := Source(testResources(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, Name)
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	confirmed := false
	err = Register(context.Background(), source, target, func() (bool, error) {
		confirmed = true
		return true, nil
	}, func(_ context.Context, got string) error {
		if got != source {
			t.Fatalf("授权源路径 = %q，期望 %q", got, source)
		}
		if err := os.Chmod(dir, 0755); err != nil {
			return err
		}
		return Link(source, target)
	})
	if err != nil || !confirmed {
		t.Fatalf("安装错误 = %v，已确认 = %t", err, confirmed)
	}
	if got, err := os.Readlink(target); err != nil || got != source {
		t.Fatalf("软链接 = %q，错误 = %v", got, err)
	}
}

func TestRegisterDoesNotAuthorizeConflicts(t *testing.T) {
	source, err := Source(testResources(t))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), Name)
	if err := os.WriteFile(target, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	err = Register(context.Background(), source, target, func() (bool, error) {
		t.Fatal("冲突时不应请求授权")
		return false, nil
	}, func(context.Context, string) error {
		t.Fatal("冲突时不应尝试授权安装")
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("冲突错误 = %v", err)
	}
}

func TestRegisterDoesNotPromptWhenDirectoryIsWritable(t *testing.T) {
	source, err := Source(testResources(t))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), Name)
	err = Register(context.Background(), source, target, func() (bool, error) {
		t.Fatal("目录可写时不应请求授权")
		return false, nil
	}, func(context.Context, string) error {
		t.Fatal("目录可写时不应尝试授权安装")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegisterStopsWhenContextIsCancelled(t *testing.T) {
	source, err := Source(testResources(t))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), Name)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Register(ctx, source, target, func() (bool, error) {
		t.Fatal("上下文已取消时不应请求授权")
		return false, nil
	}, func(context.Context, string) error {
		t.Fatal("上下文已取消时不应尝试授权安装")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误 = %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("取消后目标不应存在: %v", err)
	}
}

func TestRegisterStopsWhenAuthorizationDeclined(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 用户不受目录写入权限限制")
	}
	source, err := Source(testResources(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	err = Register(context.Background(), source, filepath.Join(dir, Name), func() (bool, error) {
		return false, nil
	}, func(context.Context, string) error {
		t.Fatal("用户拒绝时不应尝试授权安装")
		return nil
	})
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("拒绝授权错误 = %v", err)
	}
}
