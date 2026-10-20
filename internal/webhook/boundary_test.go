package webhook

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"smartstrm/internal/config"
	"sync"
	"testing"
)

func TestWithinPathDirectoryBoundaries(t *testing.T) {
	for _, tc := range []struct {
		value, prefix string
		want          bool
	}{
		{"/media/show", "/media", true}, {"/media", "/media/", true}, {"/media2/show", "/media", false},
		{"/media/../outside", "/media", false}, {"/media/show", "", false}, {"relative", "/media", false},
		{"/media\\outside", "/media", false}, {"/anything", "/", true},
	} {
		if got := withinPath(tc.value, tc.prefix); got != tc.want {
			t.Errorf("withinPath(%q,%q)=%v", tc.value, tc.prefix, got)
		}
	}
}

func TestDeleteWhitelistRejectsSiblingAndTraversalWithoutRemoteCalls(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Dir string `json:"dir"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, body.Dir)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "ok"})
	}))
	defer server.Close()
	cfg := &config.Config{Webhook: config.WebhookConfig{EmbyDeleteSync: config.EmbyDeleteSync{StrmInEmby: "/strm", StoragePathMap: "/remote", Storage: "mock", AllowedPrefix: []string{"/remote/media"}}}, Storages: []config.Storage{{Name: "mock", Driver: "openlist", URL: server.URL}}}
	h := New(cfg, nil, nil)
	for _, bad := range []string{"/strm2/media/show/file.strm", "/strm/media2/show/file.strm", "/strm/media/../outside/file.strm"} {
		if _, err := h.removeRemote(bad); err == nil {
			t.Fatalf("unsafe delete accepted: %q", bad)
		}
	}
	mu.Lock()
	before := len(calls)
	mu.Unlock()
	if before != 0 {
		t.Fatal("blocked paths reached remote", before)
	}
	remote, err := h.removeRemote("/strm/media/show/file.strm")
	if err != nil || remote != "/remote/media/show" {
		t.Fatal(remote, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != "/remote/media" {
		t.Fatal("unexpected mock deletion", calls)
	}
}
