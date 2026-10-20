package task

import (
	"path/filepath"
	"smartstrm/internal/config"
	"testing"
)

func TestPrepareChecksStorageBeforeDestruction(t *testing.T) {
	for _, mode := range []string{"missing", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{STRM: config.STRMConfig{SaveDir: t.TempDir()}, Tasks: []config.Task{{Name: "task", Storage: "disk", StoragePath: "/"}}}
			if mode == "unsupported" {
				cfg.Storages = []config.Storage{{Name: "disk", Driver: "unknown"}}
			}
			m := New(cfg, nil)
			called := false
			err := m.RunPrepared("task", func() error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("unsafe prepare: err=%v called=%v", err, called)
			}
		})
	}
}

func TestRunningSnapshotReservesDirectoriesAfterTaskDeletion(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{STRM: config.STRMConfig{SaveDir: root}, Tasks: []config.Task{{Name: "series/season", Storage: "disk", StoragePath: "/"}}, Storages: []config.Storage{{Name: "disk", Driver: "openlist", URL: "http://mock.invalid"}}}
	m := New(cfg, nil)
	// Model a run whose original task has been removed/replaced in shared configuration.
	m.activeDirs["deleted-task"] = filepath.Join(root, "series")
	for _, operation := range []string{"run", "clear"} {
		called := false
		prepare := func() error { called = true; return nil }
		var err error
		if operation == "run" {
			err = m.RunPrepared("series/season", prepare)
		} else {
			err = m.WithIdle("series/season", prepare)
		}
		if err == nil || called {
			t.Fatalf("%s overlapped active snapshot: err=%v called=%v", operation, err, called)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkActiveDir(filepath.Join(root, "series-other")); err != nil {
		t.Fatal("prefix sibling rejected", err)
	}
	if err := m.checkActiveDir(root); err == nil {
		t.Fatal("ancestor overlap accepted")
	}
}
