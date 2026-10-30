package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitRebuildRun(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.mgr.IsRunning("task") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.mgr.IsRunning("task") {
		t.Fatal("task did not finish within 5s")
	}
}

// Exercise the actual authenticated API, manager, SQLite cache, OpenList HTTP
// protocol and atomic filesystem writer, not an alternate generator stub.
func TestOverwriteRebuildSTRMOnly(t *testing.T) {
	for _, incremental := range []bool{true, false} {
		t.Run(fmt.Sprintf("incremental=%v", incremental), func(t *testing.T) {
			s, router, cfg := testAPI(t)
			var mu sync.Mutex
			downloads, childLists := 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/fs/list" {
					mu.Lock()
					downloads++
					mu.Unlock()
					fmt.Fprint(w, "remote asset with different bytes and size")
					return
				}
				if r.Method != "POST" {
					t.Errorf("upstream method=%s", r.Method)
				}
				var body struct {
					Path string `json:"path"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				var files []map[string]any
				if body.Path == "/" {
					files = []map[string]any{{"name": "child", "is_dir": true, "modified": "2026-01-01T00:00:00Z"}}
				} else if body.Path == "/child" {
					mu.Lock()
					childLists++
					mu.Unlock()
					files = []map[string]any{{"name": "movie.mp4", "is_dir": false, "size": 100}}
					for _, name := range []string{"movie.nfo", "poster.jpg", "movie.srt", "new.png"} {
						files = append(files, map[string]any{"name": name, "is_dir": false, "size": 999})
					}
				} else {
					t.Errorf("unexpected remote path %q", body.Path)
				}
				json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": files, "total": len(files)}})
			}))
			defer upstream.Close()
			next := cfg.Snapshot()
			next.Storages[0].URL = upstream.URL
			next.STRM.MediaExt = []string{"mp4"}
			next.STRM.CopyExt = []string{"nfo", "jpg", "srt", "png"}
			next.Tasks[0].Incremental = incremental
			next.Tasks[0].DirTimeCheck = true
			cfg.Replace(next)
			if err := s.db.SaveTask(next.Tasks[0]); err != nil {
				t.Fatal(err)
			}
			if err := s.mgr.Run("task"); err != nil {
				t.Fatal(err)
			}
			waitRebuildRun(t, s)
			if got := s.mgr.Status()["task"]; got.Error != "" {
				t.Fatal(got.Error)
			}
			root := filepath.Join(next.STRM.SaveDir, "task")
			preserved := map[string]string{
				"child/movie.nfo": "local nfo\x00metadata", "child/poster.jpg": "\xff\xd8local jpg",
				"child/movie.srt": "local subtitles\r\n", "nested/deep/file.bin": "nested\x00bytes",
				"orphan.strm": "orphan strm", "orphan.nfo": "orphan metadata",
			}
			write := func(rel, content string) {
				t.Helper()
				p := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			for rel, content := range preserved {
				write(rel, content)
			}
			write("child/movie.(mp4).strm", "old STRM content")
			// The missing remote-only asset must NOT be recreated by STRM-only rebuild.
			asset := filepath.Join(root, "child", "new.png")
			if err := os.Remove(asset); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "empty", "subdir"), 0755); err != nil {
				t.Fatal(err)
			}
			if incremental {
				// Prove that this exact old mtime cache really suppresses normal traversal.
				if err := s.mgr.Run("task"); err != nil {
					t.Fatal(err)
				}
				waitRebuildRun(t, s)
				b, err := os.ReadFile(filepath.Join(root, "child", "movie.(mp4).strm"))
				if err != nil || string(b) != "old STRM content" {
					t.Fatalf("cache not primed: %q %v", b, err)
				}
				if s.mgr.Status()["task"].Result.SkippedDirs != 1 {
					t.Fatal("normal run did not use primed mtime cache")
				}
			}
			mu.Lock()
			beforeDownloads, beforeLists := downloads, childLists
			mu.Unlock()
			snapshot := cfg.Snapshot()
			w := request(router, "POST", "/api/tasks/overwrite?name=task", nil)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			waitRebuildRun(t, s)
			result := s.mgr.Status()["task"]
			if result.Error != "" || result.Result.Generated != 1 || result.Result.Copied != 0 || result.Result.Removed != 0 || result.Result.SkippedDirs != 0 {
				t.Fatalf("rebuild=%+v stats=%+v", result, result.Result)
			}
			got, err := os.ReadFile(filepath.Join(root, "child", "movie.(mp4).strm"))
			want := upstream.URL + "/d/child/movie.mp4"
			if err != nil || string(got) != want {
				t.Fatalf("STRM=%q want=%q err=%v", got, want, err)
			}
			for rel, want := range preserved {
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil || string(b) != want {
					t.Fatalf("%s changed: %q err=%v", rel, b, err)
				}
			}
			if _, err := os.Stat(asset); !os.IsNotExist(err) {
				t.Fatalf("remote non-STRM was written: %v", err)
			}
			if info, err := os.Stat(filepath.Join(root, "empty", "subdir")); err != nil || !info.IsDir() {
				t.Fatalf("empty subdirectory lost: %v", err)
			}
			mu.Lock()
			afterDownloads, afterLists := downloads, childLists
			mu.Unlock()
			if afterDownloads != beforeDownloads || afterLists != beforeLists+1 {
				t.Fatalf("downloads %d->%d; child lists %d->%d", beforeDownloads, afterDownloads, beforeLists, afterLists)
			}
			stored, err := s.db.ListTasks()
			if err != nil || !reflect.DeepEqual(stored, snapshot.Tasks) || !reflect.DeepEqual(cfg.Snapshot(), snapshot) {
				t.Fatalf("configuration changed: stored=%+v err=%v", stored, err)
			}
			// No STRM-only cache may suppress normal copying after rebuild.
			if err := s.mgr.Run("task"); err != nil {
				t.Fatal(err)
			}
			waitRebuildRun(t, s)
			normal := s.mgr.Status()["task"]
			if normal.Error != "" || normal.Result.Copied != 4 {
				t.Fatalf("normal copy behavior regressed: %+v %+v", normal, normal.Result)
			}
			if !incremental {
				if _, err := os.Stat(filepath.Join(root, "orphan.strm")); !os.IsNotExist(err) {
					t.Fatal("normal sync cleanup regressed", err)
				}
			}
		})
	}
}

func TestOverwriteFailureAndCancellationPreserveFiles(t *testing.T) {
	for _, mode := range []string{"failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, router, cfg := testAPI(t)
			started := make(chan struct{})
			var once sync.Once
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Path string `json:"path"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body.Path == "/" {
					fmt.Fprint(w, `{"code":200,"data":{"content":[{"name":"child","is_dir":true,"modified":"2026-01-01T00:00:00Z"}],"total":1}}`)
					return
				}
				once.Do(func() { close(started) })
				if mode == "cancel" {
					<-r.Context().Done()
					return
				}
				http.Error(w, "upstream failed", http.StatusBadGateway)
			}))
			defer upstream.Close()
			next := cfg.Snapshot()
			next.Storages[0].URL = upstream.URL
			next.Tasks[0].Incremental = false
			next.Tasks[0].DirTimeCheck = true
			next.STRM.MediaExt = []string{"mp4"}
			cfg.Replace(next)
			if err := s.db.SaveTask(next.Tasks[0]); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(next.STRM.SaveDir, "task")
			contents := map[string]string{"child/movie.(mp4).strm": "old", "child/movie.nfo": "local", "nested/poster.jpg": "image", "movie.srt": "subtitle", "orphan.strm": "orphan"}
			for rel, content := range contents {
				p := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			w := request(router, "POST", "/api/tasks/overwrite?name=task", nil)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			// Always stop before DB/server cleanup, including failed assertions.
			defer func() {
				if s.mgr.IsRunning("task") {
					s.mgr.Stop("task")
				}
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream not reached")
			}
			if mode == "cancel" {
				for _, tool := range []string{"overwrite", "clear"} {
					w := request(router, "POST", "/api/tasks/"+tool+"?name=task", nil)
					if w.Code != 409 {
						t.Fatalf("%s: %d %s", tool, w.Code, w.Body)
					}
				}
				if err := s.mgr.Stop("task"); err != nil {
					t.Fatal(err)
				}
			}
			waitRebuildRun(t, s)
			result := s.mgr.Status()["task"]
			if result.Error == "" || result.Result.Removed != 0 {
				t.Fatalf("expected failed/stopped non-destructive run: %+v", result)
			}
			if mode == "cancel" && !strings.Contains(result.Error, "停止") {
				t.Fatal(result.Error)
			}
			for rel, want := range contents {
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil || string(b) != want {
					t.Fatalf("%s changed: %q %v", rel, b, err)
				}
			}
			stored, err := s.db.ListTasks()
			if err != nil || !reflect.DeepEqual(stored, next.Tasks) || !reflect.DeepEqual(cfg.Snapshot(), next) {
				t.Fatalf("configuration changed: %v", err)
			}
			if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if strings.HasPrefix(info.Name(), ".smartstrm-") {
					return fmt.Errorf("temporary file leaked: %s", path)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
