package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"smartstrm/internal/config"
	"smartstrm/internal/db"
	"smartstrm/internal/task"
)

func testAPI(t *testing.T) (*Server, *gin.Engine, *config.Config) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{STRM: config.STRMConfig{SaveDir: t.TempDir()}, Tasks: []config.Task{{Name: "task", Storage: "disk/one", StoragePath: "/", Incremental: true}}, Storages: []config.Storage{{Name: "disk/one", Driver: "openlist", URL: "http://mock.invalid"}}}
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := New(cfg, task.New(cfg, database), database)
	s.tokens["test"] = time.Now().Add(time.Hour)
	r := gin.New()
	s.Register(r)
	t.Cleanup(func() {
		s.mu.Lock()
		if s.stateTimer != nil {
			s.stateTimer.Stop()
		}
		s.mu.Unlock()
		s.dropSSE("")
	})
	return s, r, cfg
}
func request(r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var b bytes.Buffer
	if body != nil {
		json.NewEncoder(&b).Encode(body)
	}
	req := httptest.NewRequest(method, path, &b)
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestTaskCronAndOverlapRejectedWithoutPublication(t *testing.T) {
	_, r, cfg := testAPI(t)
	for _, name := range []string{"../escape", "task/child", "/absolute"} {
		w := request(r, "POST", "/api/tasks", config.Task{Name: name, Storage: "disk/one", StoragePath: "/"})
		if w.Code != 400 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	w := request(r, "PUT", "/api/tasks?name=task", config.Task{Storage: "disk/one", StoragePath: "/", Crontab: "bad cron"})
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	if cfg.Snapshot().Tasks[0].Crontab != "" {
		t.Fatal("invalid cron published")
	}
	w = request(r, "POST", "/api/tasks", config.Task{Name: "copy", Storage: "disk/one", StoragePath: "/"})
	if w.Code != 200 || len(cfg.Snapshot().Tasks) != 2 {
		t.Fatal(w.Code, w.Body)
	}
}

// Reproduce the old browser's PUT and the corrected copy flow without any storage I/O.
func TestRenamedTaskCopyPreservesOriginal(t *testing.T) {
	s, r, cfg := testAPI(t)
	original := cfg.Snapshot().Tasks[0]
	if err := s.db.SaveTask(original); err != nil {
		t.Fatal(err)
	}
	copyTask := original
	copyTask.Name, copyTask.StoragePath = "copy", "/changed"
	w := request(r, "PUT", "/api/tasks?name=task", copyTask)
	if w.Code != 400 {
		t.Fatalf("legacy rename must be rejected: %d %s", w.Code, w.Body)
	}
	assertTasks := func(want []config.Task) {
		t.Helper()
		stored, err := s.db.ListTasks()
		if err != nil || !reflect.DeepEqual(stored, want) || !reflect.DeepEqual(cfg.Snapshot().Tasks, want) {
			t.Fatalf("task state changed: runtime=%+v database=%+v want=%+v err=%v", cfg.Snapshot().Tasks, stored, want, err)
		}
	}
	assertTasks([]config.Task{original})
	if w = request(r, "POST", "/api/tasks", copyTask); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	assertTasks([]config.Task{original, copyTask})
	copyTask.StoragePath = "/must-not-overwrite"
	if w = request(r, "POST", "/api/tasks", copyTask); w.Code < 400 {
		t.Fatal("duplicate copy accepted", w.Code)
	}
	copyTask.StoragePath = "/changed"
	assertTasks([]config.Task{original, copyTask})
}

func TestInvalidCronPreservesDatabaseConfigAndSchedule(t *testing.T) {
	s, r, cfg := testAPI(t)
	original := cfg.Snapshot().Tasks[0]
	original.Crontab = "0 8 * * *"
	if w := request(r, "PUT", "/api/tasks?name=task", original); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	next := s.mgr.NextRuns()
	if len(next) != 1 {
		t.Fatal("original schedule was not registered", next)
	}
	for _, cron := range []string{"60 0 * * *", "0 24 * * *", "bad cron"} {
		for _, method := range []string{"POST", "PUT"} {
			draft := original
			draft.Crontab, draft.StoragePath = cron, "/changed"
			path := "/api/tasks?name=task"
			if method == "POST" {
				draft.Name = "new"
			}
			w := request(r, method, path, draft)
			if w.Code != 400 {
				t.Fatalf("%s %q: %d %s", method, cron, w.Code, w.Body)
			}
			stored, err := s.db.ListTasks()
			want := []config.Task{original}
			if err != nil || !reflect.DeepEqual(stored, want) || !reflect.DeepEqual(cfg.Snapshot().Tasks, want) || !reflect.DeepEqual(s.mgr.NextRuns(), next) {
				t.Fatalf("invalid cron changed database, config or schedule: db=%+v cfg=%+v next=%v err=%v", stored, cfg.Snapshot().Tasks, s.mgr.NextRuns(), err)
			}
		}
	}
}

func TestSlashStorageQueryRoutes(t *testing.T) {
	_, r, cfg := testAPI(t)
	w := request(r, "PUT", "/api/storages?name="+url.QueryEscape("disk/one"), config.Storage{Driver: "openlist", URL: "http://changed.invalid"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if cfg.Snapshot().Storages[0].URL != "http://changed.invalid" {
		t.Fatal("not updated")
	}
	w = request(r, "DELETE", "/api/storages?name="+url.QueryEscape("disk/one"), nil)
	if w.Code != 200 || len(cfg.Snapshot().Storages) != 0 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestClearInvalidatesCache(t *testing.T) {
	s, r, cfg := testAPI(t)
	dir := filepath.Join(cfg.Snapshot().STRM.SaveDir, "task")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old.strm"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SaveDirCache("task", map[string]int64{"/child": 20}); err != nil {
		t.Fatal(err)
	}
	w := request(r, "POST", "/api/tasks/clear?name=task", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	cache, err := s.db.LoadDirCache("task")
	if err != nil || len(cache) != 0 {
		t.Fatal(cache, err)
	}
}
func TestOverwriteRunningTaskCannotDelete(t *testing.T) {
	s, r, cfg := testAPI(t)
	started := make(chan struct{})
	var once sync.Once
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer remote.Close()
	next := cfg.Snapshot()
	next.Storages[0].URL = remote.URL
	cfg.Replace(next)
	dir := filepath.Join(next.STRM.SaveDir, "task")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dir, "keep.strm")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.mgr.Run("task"); err != nil {
		t.Fatal(err)
	}
	defer s.mgr.Stop("task")
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	for _, tool := range []string{"overwrite", "clear"} {
		w := request(r, "POST", "/api/tasks/"+tool+"?name=task", nil)
		if w.Code != 409 {
			t.Fatal(w.Code, w.Body)
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatal("running task deleted", err)
		}
	}
}
func TestConcurrentConfigReadersAndWriters(t *testing.T) {
	_, r, _ := testAPI(t)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 15; j++ {
				path := "/api/tasks"
				method := "GET"
				var body any
				if i%2 == 0 {
					path = "/api/plugins/scan_filter"
					method = "PUT"
					body = map[string]any{"enabled": j%2 == 0, "media_ext": "mp4", "unknown": map[string]any{"x": j}}
				}
				w := request(r, method, path, body)
				if w.Code != 200 {
					t.Errorf("%d %s", w.Code, w.Body)
				}
			}
		}(i)
	}
	wg.Wait()
}
func TestLogoutRevokesOnlyOwnTokenAndStreams(t *testing.T) {
	s, r, _ := testAPI(t)
	ctx, cancel := context.WithCancel(context.Background())
	other, otherCancel := context.WithCancel(context.Background())
	defer otherCancel()
	if w := request(r, "GET", "/api/tasks", nil); w.Code != 200 {
		t.Fatal("token was not usable before logout", w.Code)
	}
	s.tokens["other"] = time.Now().Add(time.Hour)
	s.sseConns[1] = sseConn{tok: "test", cancel: cancel}
	s.stateConns[1] = stateConn{tok: "other", cancel: otherCancel}
	w := request(r, "POST", "/api/logout", nil)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("own stream remains open")
	}
	select {
	case <-other.Done():
		t.Fatal("other session closed")
	default:
	}
	if w = request(r, "GET", "/api/tasks", nil); w.Code != 401 {
		t.Fatal("revoked token accepted")
	}
}
func TestStreamRegistrationRechecksRevokedToken(t *testing.T) {
	s, _, _ := testAPI(t)
	delete(s.tokens, "test")
	for _, handler := range []gin.HandlerFunc{s.stateStream, s.taskLogStream} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/", nil)
		c.Request.Header.Set("Authorization", "Bearer test")
		handler(c)
		if w.Code != 401 {
			t.Fatal("revoked token registered", w.Code)
		}
	}
}
func TestLogoutClosesLiveStateAndLogStreams(t *testing.T) {
	_, r, _ := testAPI(t)
	server := httptest.NewServer(r)
	defer server.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	var responses []*http.Response
	for _, path := range []string{"/api/events/stream", "/api/tasks/log/stream?name=task"} {
		req, _ := http.NewRequest("GET", server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer test")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %s failed to open: %d", path, resp.StatusCode)
		}
		responses = append(responses, resp)
	}
	if w := request(r, "POST", "/api/logout", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, resp := range responses {
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Fatalf("stream did not close cleanly: %v", err)
		}
	}
}
