package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsPersistenceAndCorruption(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil || s.AutoAttach() {
		t.Fatalf("默认设置: %v", err)
	}
	if err := s.SetAutoAttach(true); err != nil {
		t.Fatal(err)
	}
	s, err = Load(dir)
	if err != nil || !s.AutoAttach() {
		t.Fatalf("重启读取: %v", err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Load(dir)
	if err == nil || s.AutoAttach() {
		t.Fatal("损坏配置未报告")
	}
	if s.SetAutoAttach(true) == nil {
		t.Fatal("损坏配置被覆盖")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "bad" {
		t.Fatal("损坏配置被更改")
	}
}

func TestFailedSaveKeepsValue(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "settings.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if s.SetAutoAttach(true) == nil || s.AutoAttach() {
		t.Fatal("保存失败后设置发生变化")
	}
}
