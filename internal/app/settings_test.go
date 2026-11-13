package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"whos-typing/internal/settings"
)

func TestSettingsViewSavesToggle(t *testing.T) {
	store, err := settings.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	view := &settingsView{store: store}
	tester := ui.NewTester(view.view, 600, 300)
	tester.Frame()
	if !tester.HasText("自动允许 Agent 接入远端 Shell") {
		t.Fatalf("未显示设置: %v", tester.Texts())
	}
	if err := tester.Click("自动允许 Agent 接入远端 Shell"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !store.AutoAttach() {
		t.Fatal("开启后未保存")
	}
	if err := tester.Click("自动允许 Agent 接入远端 Shell"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if store.AutoAttach() {
		t.Fatal("关闭后未保存")
	}
}
