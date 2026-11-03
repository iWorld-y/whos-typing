// 本文件为会话和标签分配跨重启不复用的标识。
package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type idKind uint8

const (
	sessionKind idKind = iota
	tabKind
)

type idHighWater struct {
	sessions uint64
	tabs     uint64
}

// idAllocator 必须在持有控制服务单实例锁时使用。
type idAllocator struct {
	mu     sync.Mutex
	dir    string
	high   idHighWater
	failed error
	write  func(string, idHighWater) error
}

func newIDAllocator(dir string) (*idAllocator, error) {
	if err := makeStateDir(dir); err != nil {
		return nil, fmt.Errorf("创建标识状态目录失败: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("标识状态目录不安全: %s", dir)
	}
	a := &idAllocator{dir: dir, write: writeHighWater}
	path := filepath.Join(dir, "id-sequences")
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := a.write(dir, idHighWater{}); err != nil {
			return nil, fmt.Errorf("初始化标识状态失败: %w", err)
		}
		return a, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取标识状态失败: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("标识状态文件不安全")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取标识状态失败: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) != 3 || lines[2] != "" || !strings.HasPrefix(lines[0], "session=") || !strings.HasPrefix(lines[1], "tab=") {
		return nil, errors.New("标识状态已损坏")
	}
	values := []string{strings.TrimPrefix(lines[0], "session="), strings.TrimPrefix(lines[1], "tab=")}
	parsed := [2]uint64{}
	for i, value := range values {
		parsed[i], err = strconv.ParseUint(value, 10, 64)
		if err != nil || strconv.FormatUint(parsed[i], 10) != value {
			return nil, errors.New("标识状态已损坏")
		}
	}
	a.high = idHighWater{sessions: parsed[0], tabs: parsed[1]}
	return a, nil
}

// makeStateDir 创建缺失目录并同步每个父目录，避免首次安装后目录项丢失。
func makeStateDir(dir string) error {
	var missing []string
	for current := dir; ; current = filepath.Dir(current) {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("不是目录: %s", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		if filepath.Dir(current) == current {
			return err
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0700); err != nil {
			return err
		}
		if err := syncDir(filepath.Dir(missing[i])); err != nil {
			return err
		}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

func (a *idAllocator) next(kind idKind) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failed != nil {
		return "", fmt.Errorf("标识状态无法安全更新: %w", a.failed)
	}
	next := a.high
	var prefix string
	var count *uint64
	switch kind {
	case sessionKind:
		prefix, count = "session", &next.sessions
	case tabKind:
		prefix, count = "tab", &next.tabs
	default:
		return "", errors.New("未知标识类型")
	}
	if *count == ^uint64(0) {
		return "", fmt.Errorf("%s 标识空间已耗尽", prefix)
	}
	*count = *count + 1
	if err := a.write(a.dir, next); err != nil {
		// 替换文件可能已经成功，禁止本进程再次分配以免回退。
		a.failed = err
		return "", fmt.Errorf("保存标识状态失败: %w", err)
	}
	a.high = next
	return fmt.Sprintf("%s_%02d", prefix, *count), nil
}

func writeHighWater(dir string, value idHighWater) error {
	file, err := os.CreateTemp(dir, ".id-sequences-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := fmt.Fprintf(file, "session=%d\ntab=%d\n", value.sessions, value.tabs); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, "id-sequences")); err != nil {
		return err
	}
	return syncDir(dir)
}
