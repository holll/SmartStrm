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

// SSE 订阅：订阅前历史 + 订阅后增量，无丢失无乱序
func TestLogBufferSubscribe(t *testing.T) {
	b := NewLogBuffer(50000)
	b.Logf("INFO", "第1行")
	b.Logf("INFO", "第2行")

	ch, snapSeq, unsub := b.Subscribe()
	defer unsub()
	if snapSeq != 2 {
		t.Fatalf("快照 seq 应为 2，得到 %d", snapSeq)
	}

	// 订阅后写入
	b.Logf("INFO", "第3行")
	b.Logf("INFO", "第4行")

	// 历史 + 增量应完整且有序
	hist := b.History(0, snapSeq)
	var got []string
	for _, l := range hist {
		got = append(got, l.Msg)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < 4 {
		select {
		case l := <-ch:
			got = append(got, l.Msg)
		case <-time.After(time.Until(deadline)):
			t.Fatalf("等待增量超时，当前 %v", got)
		}
	}
	want := []string{"第1行", "第2行", "第3行", "第4行"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("顺序错乱: 第 %d 条 %q != %q", i, got[i], w)
		}
	}
}

// 取消订阅后不再收到推送
func TestLogBufferUnsubscribe(t *testing.T) {
	b := NewLogBuffer(50000)
	ch, _, unsub := b.Subscribe()
	unsub()
	b.Logf("INFO", "不应收到")
	select {
	case l := <-ch:
		t.Fatalf("注销后仍收到: %v", l.Msg)
	default:
	}
}
