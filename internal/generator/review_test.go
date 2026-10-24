package generator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"smartstrm/internal/config"
	"smartstrm/internal/driver"
	"testing"
	"time"
)

func TestInvalidInheritedRegexCannotScanOrClean(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		cfg, task, database, dir := setup(t)
		old := filepath.Join(dir, "keep.strm")
		put(t, old, "keep")
		cfg.Plugins = map[string]config.PluginConfig{"skip_keyword": {"enabled": true, "regex_mode": true, "filter_mode": true, "keywords": "movie"}}
		task.Plugins = map[string]config.PluginConfig{"skip_keyword": {"keywords": "["}}
		if !inherited {
			task.Plugins["skip_keyword"]["enabled"] = true
			task.Plugins["skip_keyword"]["regex_mode"] = true
		}
		called := false
		drv := mockDriver{list: func(context.Context, string) ([]driver.File, error) { called = true; return nil, nil }}
		result := New(cfg, task, drv, database).Run(context.Background())
		if called || len(result.Errors) == 0 || result.Removed != 0 {
			t.Fatalf("scan=%v result=%+v", called, result)
		}
		exists(t, old)
	}
}

func TestCacheNamespaceTracksConfigurationAndLateWriter(t *testing.T) {
	cfg, task, database, _ := setup(t)
	task.Incremental = true
	cfg.Storages = []config.Storage{{Name: "mock", URL: "http://local.invalid", Driver: "openlist"}}
	drv := mockDriver{base: "http://mock.invalid", list: func(_ context.Context, p string) ([]driver.File, error) {
		if p == "/" {
			return []driver.File{{Name: "child", IsDir: true, Modified: time.Unix(20, 0)}}, nil
		}
		return []driver.File{{Name: "movie.mp4"}}, nil
	}}
	old := New(cfg, task, drv, database)
	old.Run(context.Background())
	for _, mutate := range []func(*config.Config, *config.Task){
		func(c *config.Config, _ *config.Task) { c.STRM.SaveDir = t.TempDir() },
		func(c *config.Config, _ *config.Task) { c.STRM.URLEncode = !c.STRM.URLEncode },
		func(c *config.Config, _ *config.Task) {
			c.Plugins = map[string]config.PluginConfig{"skip_keyword": {"keywords": "foo"}}
		},
		func(c *config.Config, _ *config.Task) { c.Storages[0].Token = "new" },
		func(_ *config.Config, t *config.Task) { t.StoragePath = "/new" },
	} {
		next := cfg.Snapshot()
		copied := *task
		mutate(next, &copied)
		g := New(next, &copied, drv, database)
		if g.cacheKey() == old.cacheKey() {
			t.Fatal("configuration did not change namespace")
		}
		old.saveDirCache() // An old in-flight task finishes AFTER configuration was published.
		g.loadDirCache()
		if len(g.dirCache) != 0 {
			t.Fatal("old writer poisoned new cache")
		}
	}
	if err := database.DeleteDirCache(task.Name); err != nil {
		t.Fatal(err)
	}
	cache, err := database.LoadDirCache(old.cacheKey())
	if err != nil || len(cache) != 0 {
		t.Fatal(cache, err)
	}
}

func TestMalformedSuccessfulOpenListCannotClean(t *testing.T) {
	cfg, task, database, dir := setup(t)
	old := filepath.Join(dir, "keep.strm")
	put(t, old, "keep")
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"code":200,"data":{}}`) }))
	defer remote.Close()
	drv := driver.NewOpenList(remote.URL, "")
	if r := New(cfg, task, drv, database).Run(context.Background()); r.Removed != 0 || len(r.Errors) == 0 {
		t.Fatal(r)
	}
	exists(t, old)
}
