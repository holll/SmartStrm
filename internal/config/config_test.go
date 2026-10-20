package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestTaskPathValidation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"", "/abs", "../escape", "a/../b", "a//b", "a/.", `a\b`, "a:b", "a\x00b"} {
		if _, err := TaskDir(root, name); err == nil {
			t.Errorf("accepted unsafe name %q", name)
		}
	}
	if _, err := TaskDir(root, "tv/series"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := TaskDir(root, "link/child"); err == nil {
		t.Fatal("accepted symlink")
	}
	base := Task{Name: "tv", Storage: "mock", StoragePath: "/", Crontab: "0 8 * * *"}
	for _, name := range []string{"tv", "tv/series"} {
		task := base
		task.Name = name
		if err := ValidateTask(root, task, []Task{base}, ""); err == nil {
			t.Errorf("accepted overlap %s", name)
		}
	}
	child := base
	child.Name = "tv/series"
	if err := ValidateTask(root, base, []Task{child}, ""); err == nil {
		t.Fatal("accepted ancestor overlap")
	}
	base.Crontab = "not cron"
	if err := ValidateTask(root, base, nil, ""); err == nil {
		t.Fatal("accepted invalid cron")
	}
}

func TestSnapshotIsolationAndConcurrentPublication(t *testing.T) {
	cfg := &Config{Tasks: []Task{{Name: "task", Plugins: map[string]PluginConfig{"p": {"nested": map[string]any{"items": []any{"original"}}}}}}, Plugins: map[string]PluginConfig{"p": {"enabled": true}}}
	copy := cfg.Snapshot()
	copy.Tasks[0].Plugins["p"]["nested"].(map[string]any)["items"].([]any)[0] = "changed"
	if got := cfg.Snapshot().Tasks[0].Plugins["p"]["nested"].(map[string]any)["items"].([]any)[0]; got != "original" {
		t.Fatalf("snapshot aliases %v", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				next := cfg.Snapshot()
				next.Plugins["p"]["enabled"] = j%2 == 0
				cfg.Replace(next)
				_ = cfg.Snapshot()
			}
		}()
	}
	wg.Wait()
}
