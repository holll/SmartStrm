package task

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"smartstrm/internal/config"
	"smartstrm/internal/db"
)

func TestRunArchiveBeyondLiveLimitIsFrozenAndPersistent(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "logs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	b := NewLogBuffer(logBufferMax)
	b.retain = true
	for i := 0; i < logBufferMax+100; i++ {
		b.Logf("INFO", "line %d", i)
	}
	lines := b.Freeze()
	b.Logf("INFO", "late write")
	if len(lines) != logBufferMax+100 || lines[0].Msg != "line 0" || b.LastSeq() != int64(len(lines)) {
		t.Fatal("archive truncated or not frozen")
	}
	id, err := database.CreateRun("archive")
	if err != nil {
		t.Fatal(err)
	}
	stored := make([]struct {
		Time  time.Time
		Level string
		Msg   string
	}, len(lines))
	for i, l := range lines {
		stored[i] = struct {
			Time  time.Time
			Level string
			Msg   string
		}{l.Time, l.Level, l.Msg}
	}
	if err := database.InsertLogs(id, stored); err != nil {
		t.Fatal(err)
	}
	got, err := database.RunLogs(id)
	if err != nil || len(got) != len(lines) || got[0].Msg != "line 0" {
		t.Fatal(len(got), err)
	}
	next := NewLogBuffer(logBufferMax)
	next.retain = true
	next.seq = b.LastSeq()
	next.Logf("INFO", "next")
	if len(next.Freeze()) != 1 || lines[len(lines)-1].Msg != fmt.Sprintf("line %d", logBufferMax+99) {
		t.Fatal("runs mixed")
	}
}

func TestRunArchivePersistsInBoundedBatchesAndRetriesFailures(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			database, err := db.Open(filepath.Join(t.TempDir(), "batch.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			id, err := database.CreateRun("batch")
			if err != nil {
				t.Fatal(err)
			}
			b := NewLogBuffer(100)
			b.retain = true
			calls := 0
			b.persist = func(lines []LogLine) error {
				calls++
				if fail {
					return fmt.Errorf("injected transaction error")
				}
				return persistRunLogs(database, id, lines)
			}
			for i := 0; i < 5100; i++ {
				b.Logf("INFO", "line %d", i)
			}
			if !fail && len(b.archive) != 100 {
				t.Fatal("unbounded pending archive", len(b.archive))
			}
			if fail && (len(b.archive) != 5100 || calls != 1) {
				t.Fatal("lost failed batch", len(b.archive), calls)
			}
			if err := persistRunLogs(database, id, b.Freeze()); err != nil {
				t.Fatal(err)
			}
			lines, err := database.RunLogs(id)
			if err != nil || len(lines) != 5100 || lines[0].Msg != "line 0" || lines[5099].Msg != "line 5099" {
				t.Fatal("lost/mixed/duplicated lines", len(lines), err)
			}
			ch, _, unsub := b.Subscribe()
			defer unsub()
			select {
			case _, ok := <-ch:
				if ok {
					t.Fatal("frozen subscriber still live")
				}
			default:
				t.Fatal("frozen subscriber cannot reconnect")
			}
		})
	}
}

func TestFinalizationDatabaseErrorsRemainVisibleAndFreezeBeforeIdle(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, `{"code":200,"data":{"content":[],"total":0}}`)
	}))
	defer remote.Close()
	cfg := &config.Config{STRM: config.STRMConfig{SaveDir: t.TempDir()}, Tasks: []config.Task{{Name: "t", Storage: "s", StoragePath: "/"}}, Storages: []config.Storage{{Name: "s", Driver: "openlist", URL: remote.URL}}}
	database, err := db.Open(filepath.Join(t.TempDir(), "failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	m := New(cfg, database)
	if err := m.Run("t"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("scan never started")
	}
	database.Close()
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for m.IsRunning("t") {
		if time.Now().After(deadline) {
			t.Fatal("failed finalizer stuck")
		}
		time.Sleep(time.Millisecond)
	}
	m.mu.Lock()
	result := m.results["t"]
	m.mu.Unlock()
	if !strings.Contains(result.Error, "保存运行日志失败") || !strings.Contains(result.Error, "保存运行结果失败") {
		t.Fatal(result.Error)
	}
	b := m.Log("t")
	seq := b.LastSeq()
	b.Logf("INFO", "late")
	if seq != b.LastSeq() {
		t.Fatal("idle published before buffer froze")
	}
}

func TestSlowLogSubscriberMustResynchronize(t *testing.T) {
	b := NewLogBuffer(2000)
	ch, _, unsub := b.Subscribe()
	defer unsub()
	for i := 0; i < 1100; i++ {
		b.Logf("INFO", "%d", i)
	}
	n := 0
	for range ch {
		n++
	}
	if n != 1024 {
		t.Fatal(n)
	}
	b.mu.Lock()
	remaining := len(b.subs)
	b.mu.Unlock()
	if remaining != 0 {
		t.Fatal("overflowed subscriber still live")
	}
	history := b.Since(0)
	if len(history) != 1100 {
		t.Fatal("reconnect cannot recover", len(history))
	}
}

func TestInvalidRegexAndCreateRunFailurePrecedePreparation(t *testing.T) {
	for _, mode := range []string{"regex", "closed database"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{STRM: config.STRMConfig{SaveDir: t.TempDir()}, Tasks: []config.Task{{Name: "t", Storage: "s", StoragePath: "/"}}, Storages: []config.Storage{{Name: "s", Driver: "openlist", URL: "http://127.0.0.1:1"}}}
			database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if mode == "regex" {
				cfg.Plugins = map[string]config.PluginConfig{"skip_keyword": {"enabled": true, "regex_mode": true, "keywords": "["}}
			} else {
				database.Close()
			}
			m := New(cfg, database)
			prepared := false
			err = m.RunPrepared("t", func() error { prepared = true; return nil })
			if err == nil || prepared || m.IsRunning("t") {
				t.Fatalf("err=%v prepared=%v", err, prepared)
			}
		})
	}
}

func TestSequentialRunsHaveIndependentDurableLogs(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":200,"data":{"content":[],"total":0}}`)
	}))
	defer remote.Close()
	cfg := &config.Config{STRM: config.STRMConfig{SaveDir: t.TempDir()}, Tasks: []config.Task{{Name: "t", Storage: "s", StoragePath: "/"}}, Storages: []config.Storage{{Name: "s", Driver: "openlist", URL: remote.URL}}}
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	m := New(cfg, database)
	for i := 0; i < 2; i++ {
		if err := m.Run("t"); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for m.IsRunning("t") {
			if time.Now().After(deadline) {
				t.Fatal("run did not finish")
			}
			time.Sleep(time.Millisecond)
		}
	}
	runs, err := database.RecentRuns(10)
	if err != nil || len(runs) != 2 {
		t.Fatal(runs, err)
	}
	for _, r := range runs {
		lines, err := database.RunLogs(r.ID)
		if err != nil || len(lines) == 0 || r.Status != "success" {
			t.Fatal(r, lines, err)
		}
		count := 0
		for _, l := range lines {
			if strings.Contains(l.Msg, "开始任务:") {
				count++
			}
		}
		if count != 1 {
			t.Fatal("mixed runs", count)
		}
	}
}
