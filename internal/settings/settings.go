// Package settings 管理本机应用偏好。
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type values struct {
	AutoAttach bool `json:"auto_attach"`
}

// Store 保存自动接入偏好，损坏的文件不会被覆盖。
type Store struct {
	mu      sync.Mutex
	path    string
	value   values
	loadErr error
}

// Load 读取指定目录的偏好，缺失文件使用默认值。
func Load(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "settings.json")}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &s.value)
	}
	if err != nil {
		s.value = values{}
		s.loadErr = fmt.Errorf("读取设置失败: %w", err)
	}
	return s, s.loadErr
}

// AutoAttach 返回当前生效的设置。
func (s *Store) AutoAttach() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.value.AutoAttach }

// SetAutoAttach 原子保存后才更新内存中的生效值。
func (s *Store) SetAutoAttach(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return s.loadErr
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建设置目录失败: %w", err)
	}
	data, err := json.Marshal(values{AutoAttach: enabled})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return fmt.Errorf("保存设置失败: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.path)
	}
	if err != nil {
		return fmt.Errorf("保存设置失败: %w", err)
	}
	s.value.AutoAttach = enabled
	return nil
}
