package generator

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"smartstrm/internal/config"
	"smartstrm/internal/db"
	"smartstrm/internal/driver"
)

type mockDriver struct {
	list func(context.Context, string) ([]driver.File, error)
	base string
}

func (m mockDriver) List(c context.Context, p string) ([]driver.File, error) { return m.list(c, p) }
func (m mockDriver) Remove(context.Context, string) error {
	return errors.New("unexpected remote deletion")
}
func (m mockDriver) Rename(context.Context, string, string) error {
	return errors.New("unexpected remote rename")
}
func (m mockDriver) Base() string { return m.base }

func setup(t *testing.T) (*config.Config, *config.Task, *db.DB, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{STRM: config.STRMConfig{SaveDir: root, MediaExt: []string{"mp4"}, CopyExt: []string{"nfo"}}}
	task := &config.Task{Name: "test", Storage: "mock", StoragePath: "/", DirTimeCheck: true}
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	dir := filepath.Join(root, task.Name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return cfg, task, database, dir
}
func put(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0644); err != nil {
		t.Fatal(err)
	}
}
func exists(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestFailedOrCancelledScanNeverCleans(t *testing.T) {
	for _, mode := range []string{"root failure", "child failure", "cancelled", "output failure"} {
		t.Run(mode, func(t *testing.T) {
			cfg, task, database, dir := setup(t)
			old := filepath.Join(dir, "old.strm")
			put(t, old, "keep")
			if _, err := database.LoadDirCacheNamespace(task.Name, New(cfg, task, mockDriver{}, database).cacheKey()); err != nil {
				t.Fatal(err)
			}
			if err := database.SaveDirCache(New(cfg, task, mockDriver{}, database).cacheKey(), map[string]int64{"/child": 1}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			drv := mockDriver{list: func(_ context.Context, p string) ([]driver.File, error) {
				switch mode {
				case "root failure":
					return nil, errors.New("mock list failure")
				case "child failure":
					if p == "/" {
						return []driver.File{{Name: "child", IsDir: true, Modified: time.Unix(2, 0)}}, nil
					}
					return nil, errors.New("mock child failure")
				case "cancelled":
					cancel()
					return nil, nil
				default:
					return []driver.File{{Name: "bad/name.mp4"}}, nil
				}
			}}
			res := New(cfg, task, drv, database).Run(ctx)
			exists(t, old)
			if res.Removed != 0 {
				t.Fatal("removed after failed scan")
			}
			cache, err := database.LoadDirCache(New(cfg, task, mockDriver{}, database).cacheKey())
			if err != nil {
				t.Fatal(err)
			}
			if len(cache) != 0 {
				t.Fatalf("failed scan cached: %v", cache)
			}
		})
	}
}

func TestSyncMtimeDoesNotDeleteSkippedSubtree(t *testing.T) {
	cfg, task, database, dir := setup(t)
	put(t, filepath.Join(dir, "child", "movie.(mp4).strm"), "old")
	put(t, filepath.Join(dir, "stale.strm"), "stale")
	if _, err := database.LoadDirCacheNamespace(task.Name, New(cfg, task, mockDriver{}, database).cacheKey()); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveDirCache(New(cfg, task, mockDriver{}, database).cacheKey(), map[string]int64{"/child": 20}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	drv := mockDriver{base: "http://mock.invalid", list: func(_ context.Context, p string) ([]driver.File, error) {
		calls++
		if p == "/" {
			return []driver.File{{Name: "child", IsDir: true, Modified: time.Unix(20, 0)}}, nil
		}
		return []driver.File{{Name: "movie.mp4", Size: 100}}, nil
	}}
	res := New(cfg, task, drv, database).Run(context.Background())
	if len(res.Errors) != 0 {
		t.Fatal(res.Errors)
	}
	if calls != 2 {
		t.Fatalf("sync skipped scan: calls=%d", calls)
	}
	exists(t, filepath.Join(dir, "child", "movie.(mp4).strm"))
	if res.Removed != 1 {
		t.Fatalf("removed=%d", res.Removed)
	}
}

func TestWriteFailureDoesNotCacheDirectory(t *testing.T) {
	cfg, task, database, dir := setup(t)
	task.Incremental = true
	if err := os.MkdirAll(filepath.Join(dir, "child", "movie.(mp4).strm"), 0755); err != nil {
		t.Fatal(err)
	}
	drv := mockDriver{base: "http://mock.invalid", list: func(_ context.Context, p string) ([]driver.File, error) {
		if p == "/" {
			return []driver.File{{Name: "child", IsDir: true, Modified: time.Unix(20, 0)}}, nil
		}
		return []driver.File{{Name: "movie.mp4"}}, nil
	}}
	res := New(cfg, task, drv, database).Run(context.Background())
	if len(res.Errors) == 0 {
		t.Fatal("expected rename failure")
	}
	cache, _ := database.LoadDirCache(New(cfg, task, mockDriver{}, database).cacheKey())
	if len(cache) != 0 {
		t.Fatal(cache)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "child", ".smartstrm-*"))
	if len(matches) != 0 {
		t.Fatal("temporary file leaked", matches)
	}
}

type failingCloser struct{ err error }

func (c failingCloser) Close() error { return c.err }
func TestFinishCopyErrors(t *testing.T) {
	for _, mode := range []string{"write", "close", "rename"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			tmp := filepath.Join(dir, "tmp")
			dst := filepath.Join(dir, "dst")
			put(t, tmp, "partial")
			put(t, dst, "original")
			boom := errors.New(mode)
			var writeErr, closeErr error
			if mode == "write" {
				writeErr = boom
			}
			if mode == "close" {
				closeErr = boom
			}
			renamed := false
			err := finishCopy(failingCloser{closeErr}, writeErr, tmp, dst, func(string, string) error { renamed = true; return boom })
			if !errors.Is(err, boom) {
				t.Fatal(err)
			}
			if renamed != (mode == "rename") {
				t.Fatal("published before successful close")
			}
			if _, err := os.Stat(tmp); !os.IsNotExist(err) {
				t.Fatal("temporary file leaked")
			}
			b, _ := os.ReadFile(dst)
			if string(b) != "original" {
				t.Fatal("existing destination destroyed")
			}
		})
	}
}

func TestAtomicCopyRemainsReadable(t *testing.T) {
	cfg, task, database, dir := setup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("asset")) }))
	defer server.Close()
	drv := mockDriver{base: server.URL, list: func(context.Context, string) ([]driver.File, error) {
		return []driver.File{{Name: "movie.nfo", Size: 5}}, nil
	}}
	result := New(cfg, task, drv, database).Run(context.Background())
	if len(result.Errors) != 0 || result.Copied != 1 {
		t.Fatal(result)
	}
	info, err := os.Stat(filepath.Join(dir, "movie.nfo"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("asset permissions: %o", info.Mode().Perm())
	}
}

func TestDownloadAndDelayCancellation(t *testing.T) {
	cfg, task, database, dir := setup(t)
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer srv.Close()
	drv := mockDriver{base: srv.URL, list: func(context.Context, string) ([]driver.File, error) {
		return []driver.File{{Name: "movie.nfo", Size: 100}}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan *Result, 1)
	go func() { done <- New(cfg, task, drv, database).Run(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("download not started")
	}
	cancel()
	select {
	case res := <-done:
		if res.Copied != 0 || len(res.Errors) == 0 {
			t.Fatal(res)
		}
	case <-time.After(time.Second):
		t.Fatal("download ignored cancellation")
	}
	if _, err := os.Stat(filepath.Join(dir, "movie.nfo")); !os.IsNotExist(err) {
		t.Fatal("cancelled output published")
	}
	begin := time.Now()
	if waitContext(ctx, time.Hour) {
		t.Fatal("cancelled delay succeeded")
	}
	if time.Since(begin) > time.Second {
		t.Fatal("delay ignored cancellation")
	}
}
