package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testIDs(t *testing.T) *idAllocator {
	t.Helper()
	a, err := newIDAllocator(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestIDAllocatorSeparateSequencesAndDecimalBoundary(t *testing.T) {
	a := testIDs(t)
	for _, tc := range []struct {
		kind idKind
		want string
	}{
		{sessionKind, "session_01"}, {tabKind, "tab_01"},
		{sessionKind, "session_02"}, {tabKind, "tab_02"},
	} {
		id, err := a.next(tc.kind)
		if err != nil || id != tc.want {
			t.Fatalf("标识 = %q，期望 %q，错误 %v", id, tc.want, err)
		}
	}
	if err := writeHighWater(a.dir, idHighWater{sessions: 8, tabs: 8}); err != nil {
		t.Fatal(err)
	}
	a, err := newIDAllocator(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind idKind
		want string
	}{
		{sessionKind, "session_09"}, {sessionKind, "session_10"},
		{tabKind, "tab_09"}, {tabKind, "tab_10"},
	} {
		id, err := a.next(tc.kind)
		if err != nil || id != tc.want {
			t.Fatalf("标识 = %q，期望 %q，错误 %v", id, tc.want, err)
		}
	}
	if err := writeHighWater(a.dir, idHighWater{sessions: 98, tabs: 98}); err != nil {
		t.Fatal(err)
	}
	a, err = newIDAllocator(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind idKind
		want string
	}{
		{sessionKind, "session_99"}, {sessionKind, "session_100"},
		{tabKind, "tab_99"}, {tabKind, "tab_100"},
	} {
		id, err := a.next(tc.kind)
		if err != nil || id != tc.want {
			t.Fatalf("标识 = %q，期望 %q，错误 %v", id, tc.want, err)
		}
	}
}

func TestIDAllocatorConcurrentAndRestart(t *testing.T) {
	a := testIDs(t)
	const count = 60
	ids := make(chan string, count*2)
	var wg sync.WaitGroup
	for _, kind := range []idKind{sessionKind, tabKind} {
		for i := 0; i < count; i++ {
			wg.Add(1)
			go func(kind idKind) {
				defer wg.Done()
				id, err := a.next(kind)
				if err != nil {
					t.Error(err)
					return
				}
				ids <- id
			}(kind)
		}
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("重复标识: %q", id)
		}
		seen[id] = true
	}
	for i := 1; i <= count; i++ {
		for _, prefix := range []string{"session", "tab"} {
			id := fmt.Sprintf("%s_%02d", prefix, i)
			if !seen[id] {
				t.Fatalf("缺少标识: %s", id)
			}
		}
	}
	restarted, err := newIDAllocator(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind idKind
		want string
	}{{sessionKind, "session_61"}, {tabKind, "tab_61"}} {
		id, err := restarted.next(tc.kind)
		if err != nil || id != tc.want || seen[id] {
			t.Fatalf("重启后标识错误: %q %v", id, err)
		}
	}
	info, err := os.Stat(filepath.Join(a.dir, "id-sequences"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("状态权限错误: %v %v", info, err)
	}
}

func TestIDAllocatorPreservesLegacyFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "id-high-water")
	const legacyValue = "损坏的旧状态也不得覆盖"
	if err := os.WriteFile(legacy, []byte(legacyValue), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newIDAllocator(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind idKind
		want string
	}{{sessionKind, "session_01"}, {tabKind, "tab_01"}} {
		id, err := a.next(tc.kind)
		if err != nil || id != tc.want {
			t.Fatalf("新命名空间错误: %q %v", id, err)
		}
	}
	if _, err := newIDAllocator(dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(legacy)
	if err != nil || string(got) != legacyValue {
		t.Fatalf("旧状态被修改: %q %v", got, err)
	}
}

func TestIDAllocatorCorruptAndExhausted(t *testing.T) {
	for _, value := range []string{"", "session=0\n", "session=-1\ntab=0\n", "session=01\ntab=0\n", "session=0\ntab=x\n", "tab=0\nsession=0\n", "session=18446744073709551616\ntab=0\n", "session=0\ntab=0\nextra"} {
		dir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "id-sequences"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := newIDAllocator(dir); err == nil {
			t.Fatalf("未拒绝损坏状态 %q", value)
		}
	}
	for _, tc := range []struct {
		high                 idHighWater
		exhausted, available idKind
		want                 string
	}{
		{idHighWater{sessions: ^uint64(0)}, sessionKind, tabKind, "tab_01"},
		{idHighWater{tabs: ^uint64(0)}, tabKind, sessionKind, "session_01"},
	} {
		a := testIDs(t)
		if err := writeHighWater(a.dir, tc.high); err != nil {
			t.Fatal(err)
		}
		a, err := newIDAllocator(a.dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.next(tc.exhausted); err == nil || !strings.Contains(err.Error(), "耗尽") {
			t.Fatalf("耗尽未报告: %v", err)
		}
		id, err := a.next(tc.available)
		if err != nil || id != tc.want {
			t.Fatalf("另一序列无法使用: %q %v", id, err)
		}
	}
}

func TestIDAllocatorWriteFailureDoesNotReuse(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(fmt.Sprintf("afterRename=%t", afterRename), func(t *testing.T) {
			a := testIDs(t)
			a.write = func(dir string, value idHighWater) error {
				if afterRename {
					if err := writeHighWater(dir, value); err != nil {
						return err
					}
				}
				return errors.New("模拟持久化失败")
			}
			if _, err := a.next(sessionKind); err == nil {
				t.Fatal("写入失败未报告")
			}
			if _, err := a.next(tabKind); err == nil {
				t.Fatal("失败后仍允许分配")
			}
			restarted, err := newIDAllocator(a.dir)
			if err != nil {
				t.Fatal(err)
			}
			want := "session_01"
			if afterRename {
				want = "session_02"
			}
			id, err := restarted.next(sessionKind)
			if err != nil || id != want {
				t.Fatalf("重启后高水位错误: %q %v", id, err)
			}
			id, err = restarted.next(tabKind)
			if err != nil || id != "tab_01" {
				t.Fatalf("另一序列高水位错误: %q %v", id, err)
			}
		})
	}
}
