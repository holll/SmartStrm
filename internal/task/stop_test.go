package task

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"smartstrm/internal/config"
)

func TestStopThenRunningFlag(t *testing.T) {
	// 模拟 OpenList：第一页 200 条（触发分页），第二页慢速返回 0 条
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/fs/list" {
			var req struct {
				Page int `json:"page"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Page == 1 {
				var content []map[string]any
				for i := 0; i < 200; i++ {
					content = append(content, map[string]any{
						"name": "f" + string(rune('A'+i%26)) + string(rune('a'+i%26)) + ".mp4",
						"size": 1 << 20, "is_dir": false, "modified": "2026-08-11 10:00:00",
					})
				}
				json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "ok",
					"data": map[string]any{"content": content}})
				return
			}
			// 第二页：慢速响应，模拟扫描中（客户端取消后立即退出）
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Second):
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "ok",
				"data": map[string]any{"content": []any{}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "ok",
			"data": map[string]any{"raw_url": "http://x/d/f.mp4"}})
	}))
	defer srv.Close()

	dir, _ := os.MkdirTemp("", "stop-test")
	defer os.RemoveAll(dir)

	cfg := &config.Config{
		STRM: config.STRMConfig{SaveDir: dir},
		Tasks: []config.Task{
			{Name: "t1", Storage: "s1", StoragePath: "/", Incremental: true, DirTimeCheck: true},
		},
		Storages: []config.Storage{{Name: "s1", Driver: "openlist", URL: srv.URL, Token: ""}},
	}
	m := New(cfg, nil)
	if err := m.Run("t1"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond) // 等第一页处理完，进入第二页慢速请求
	if !m.IsRunning("t1") {
		t.Fatal("任务应在运行中")
	}
	stopAt := time.Now()
	if err := m.Stop("t1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for m.IsRunning("t1") {
		if time.Now().After(deadline) {
			t.Fatal("停止后 8s 仍显示运行中（bug 复现）")
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("Stop 后 %.2fs running 标志清零", time.Since(stopAt).Seconds())
}
