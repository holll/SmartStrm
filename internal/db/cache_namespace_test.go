package db

import (
	"path/filepath"
	"testing"
)

func TestCacheNamespaceTransitionsRejectLateWriterAndRevertedConfig(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	const task = "task"
	a, b := task+"\x00A", task+"\x00B"
	if _, err = d.LoadDirCacheNamespace(task, a); err != nil {
		t.Fatal(err)
	}
	if err = d.SaveDirCacheNamespace(task, a, map[string]int64{"/old": 1}); err != nil {
		t.Fatal(err)
	}
	got, err := d.LoadDirCacheNamespace(task, a)
	if err != nil || got["/old"] != 1 {
		t.Fatal("unchanged config missed cache", got, err)
	}
	got, err = d.LoadDirCacheNamespace(task, b)
	if err != nil || len(got) != 0 {
		t.Fatal("changed config reused cache", got, err)
	}
	if err = d.SaveDirCacheNamespace(task, a, map[string]int64{"/stale": 9}); err != nil {
		t.Fatal(err)
	}
	got, err = d.LoadDirCache(a)
	if err != nil || len(got) != 0 {
		t.Fatal("late old writer restored obsolete cache", got, err)
	}
	if err = d.SaveDirCacheNamespace(task, b, map[string]int64{"/new": 2}); err != nil {
		t.Fatal(err)
	}
	got, err = d.LoadDirCacheNamespace(task, a)
	if err != nil || len(got) != 0 {
		t.Fatal("A-B-A reused historic cache", got, err)
	}
	if err = d.SaveDirCacheNamespace(task, b, map[string]int64{"/stale": 9}); err != nil {
		t.Fatal(err)
	}
	got, err = d.LoadDirCache(b)
	if err != nil || len(got) != 0 {
		t.Fatal("late B writer restored obsolete cache", got, err)
	}
}
