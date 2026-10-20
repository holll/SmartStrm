package plugins

import (
	"smartstrm/internal/config"
	"testing"
)

func TestMergeConfigInheritsFieldsAndIsolatesMaps(t *testing.T) {
	global := map[string]config.PluginConfig{"p": {"enabled": true, "find_text": "old", "replace_text": "new", "nested": map[string]any{"x": "original"}}}
	task := map[string]config.PluginConfig{"p": {"replace_text": "task"}, "unknown": {"enabled": false}}
	merged := MergeConfig(global, task)
	if merged["p"]["enabled"] != true || merged["p"]["find_text"] != "old" || merged["p"]["replace_text"] != "task" {
		t.Fatal(merged)
	}
	merged["p"]["nested"].(map[string]any)["x"] = "changed"
	if global["p"]["nested"].(map[string]any)["x"] != "original" {
		t.Fatal("mutated global")
	}
	task["p"]["enabled"] = false
	if MergeConfig(global, task)["p"]["enabled"] != false {
		t.Fatal("explicit disable not respected")
	}
	if merged["unknown"]["enabled"] != false {
		t.Fatal("unknown plugin lost")
	}
}
