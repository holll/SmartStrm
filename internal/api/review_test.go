package api

import (
	"bytes"
	"context"
	"net/http/httptest"
	"reflect"
	"smartstrm/internal/config"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInvalidRegexSaveRejectedWithoutConfigOrDatabaseChange(t *testing.T) {
	s, r, cfg := testAPI(t)
	global := cfg.Snapshot()
	global.Plugins = map[string]config.PluginConfig{"skip_keyword": {"enabled": true, "regex_mode": true, "filter_mode": true, "keywords": "ok"}}
	cfg.Replace(global)
	original := cfg.Snapshot().Tasks[0]
	if err := s.db.SaveTask(original); err != nil {
		t.Fatal(err)
	}
	updated := original
	updated.Plugins = map[string]config.PluginConfig{"skip_keyword": {"keywords": "["}}
	if w := request(r, "PUT", "/api/tasks?name=task", updated); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	if !reflect.DeepEqual(original, cfg.Snapshot().Tasks[0]) {
		t.Fatal("invalid task published")
	}
	if w := request(r, "PUT", "/api/plugins/skip_keyword", map[string]any{"enabled": true, "regex_mode": true, "keywords": "["}); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
	if cfg.Snapshot().Plugins["skip_keyword"]["keywords"] != "ok" {
		t.Fatal("invalid global plugin published")
	}
	// A valid partial global update may activate an invalid inherited task regex.
	snapshot := cfg.Snapshot()
	snapshot.Plugins["skip_keyword"]["enabled"] = false
	snapshot.Tasks[0].Plugins = map[string]config.PluginConfig{"skip_keyword": {"keywords": "["}}
	cfg.Replace(snapshot)
	if w := request(r, "PUT", "/api/plugins/skip_keyword", map[string]any{"enabled": true, "regex_mode": true, "keywords": "ok"}); w.Code != 400 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestStorageDeletionReferenceAndDatabaseFailureAreAtomic(t *testing.T) {
	s, r, cfg := testAPI(t)
	storage := cfg.Snapshot().Storages[0]
	if err := s.db.SaveStorage(storage); err != nil {
		t.Fatal(err)
	}
	if w := request(r, "DELETE", "/api/storages?name=disk%2Fone", nil); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	if got, err := s.db.ListStorages(); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	snapshot := cfg.Snapshot()
	snapshot.Tasks = nil
	snapshot.Webhook.EmbyDeleteSync.Storage = storage.Name
	cfg.Replace(snapshot)
	if w := request(r, "DELETE", "/api/storages?name=disk%2Fone", nil); w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	snapshot.Webhook.EmbyDeleteSync.Storage = ""
	cfg.Replace(snapshot)
	s.db.Close()
	if w := request(r, "DELETE", "/api/storages?name=disk%2Fone", nil); w.Code != 500 {
		t.Fatal(w.Code, w.Body)
	}
	if len(cfg.Snapshot().Storages) != 1 {
		t.Fatal("DB failure removed live config")
	}
}

func TestLoginGlobalRateLimitCannotBeBypassedWithForwardedIPs(t *testing.T) {
	s, r, _ := testAPI(t)
	for i := 0; i < 21; i++ {
		req := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"not-admin","password":"bad"}`))
		req.RemoteAddr = "192.0.2.10:1234"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", strings.Repeat("1", i+1)+".2.3.4")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if i < 20 && w.Code != 401 {
			t.Fatal(i, w.Code, w.Body)
		}
		if i == 20 && (w.Code != 429 || w.Header().Get("Retry-After") == "") {
			t.Fatal(w.Code, w.Body)
		}
	}
	audits, err := s.db.RecentAudits(50)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range audits {
		if a.Action == "login_failed" && a.Target != "192.0.2.10" {
			t.Fatal("trusted spoofed IP", a)
		}
	}
	s.mu.Lock()
	s.loginWindow = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	if w := request(r, "POST", "/api/login", map[string]string{"username": "wrong"}); w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
}

type blockedLoginBody struct {
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
	reader  *strings.Reader
}

func (b *blockedLoginBody) Read(p []byte) (int, error) {
	b.once.Do(func() { b.started <- struct{}{}; <-b.release })
	return b.reader.Read(p)
}
func (b *blockedLoginBody) Close() error { return nil }

func TestLoginConcurrencyBudgetAndBodyCaps(t *testing.T) {
	_, r, _ := testAPI(t)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/login", nil)
			req.Body = &blockedLoginBody{started: started, release: release, reader: strings.NewReader(`{"username":"wrong"}`)}
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("login did not reach body read")
		}
	}
	w := request(r, "POST", "/api/login", map[string]string{"username": "wrong"})
	close(release)
	wg.Wait()
	if w.Code != 429 {
		t.Fatal(w.Code, w.Body)
	}
	for _, tc := range []struct {
		path string
		n    int
	}{{"/api/login", 4097}, {"/api/tasks", (1 << 20) + 1}} {
		req := httptest.NewRequest("POST", tc.path, bytes.NewReader(bytes.Repeat([]byte("x"), tc.n)))
		req.Header.Set("Authorization", "Bearer test")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 413 {
			t.Fatal(tc.path, w.Code, w.Body)
		}
	}
}

func TestSSETruncationHasExplicitResyncNotice(t *testing.T) {
	s, r, _ := testAPI(t)
	b := s.mgr.Log("task")
	for i := 0; i < 50010; i++ {
		b.Logf("INFO", "line %d", i)
	}
	// Cancelled context allows deterministic frame inspection without a real socket.
	req := httptest.NewRequest("GET", "/api/tasks/log/stream?name=task&after=1", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "event: resync\ndata: {\"truncated\":true}") {
		t.Fatal("missing truncation notice")
	}
}
