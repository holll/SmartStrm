package webhook

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"smartstrm/internal/config"
	"smartstrm/internal/db"
	"smartstrm/internal/task"
)

// embyLogTestDB 构造带真实 db 的测试路由
func embyLogTestDB(t *testing.T, es config.EmbyDeleteSync) (*gin.Engine, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	cfg := &config.Config{
		Webhook: config.WebhookConfig{Token: "test-token-123", EmbyDeleteSync: es},
	}
	h := New(cfg, task.New(cfg, nil), database)
	r := gin.New()
	h.Register(r)
	return r, database
}

// 未启用时：返回 ignored，且记录 skipped 日志（含完整通知 payload）
func TestWebhookLogEmbySkipped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r, database := embyLogTestDB(t, config.EmbyDeleteSync{Enabled: false, StrmInEmby: "/strm"})
	defer database.Close()

	body := `{"Event":"library.deleted","Item":{"Path":"/strm/AV/T/T-038/T-038-029.strm"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/test-token-123", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "ignored: emby 删除同步未启用" {
		t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
	}

	logs, err := database.RecentWebhookLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("期望 1 条日志，got %d", len(logs))
	}
	l := logs[0]
	if l.Kind != "emby" || l.Event != "library.deleted" || l.Action != "emby_delete" ||
		l.Result != "skipped" || l.Target != "/strm/AV/T/T-038/T-038-029.strm" {
		t.Fatalf("日志内容不符: %+v", l)
	}
	if !strings.Contains(l.Payload, "library.deleted") {
		t.Fatalf("payload 应为完整通知: %q", l.Payload)
	}
}

// 已启用但配置不完整：白名单拦截返回 500，记录 failed 日志（含映射后的远端路径与错误）
func TestWebhookLogEmbyFailed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	es := config.EmbyDeleteSync{
		Enabled: true, StrmInEmby: "/strm", Storage: "nope",
		StoragePathMap: "/115", AllowedPrefix: nil,
	}
	r, database := embyLogTestDB(t, es)
	defer database.Close()

	body := `{"Event":"library.deleted","Item":{"Path":"/strm/AV/T/T-038/T-038-029.strm"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/test-token-123", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("期望 500，got %d body=%q", w.Code, w.Body.String())
	}

	logs, err := database.RecentWebhookLogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("期望 1 条日志，got %d", len(logs))
	}
	l := logs[0]
	if l.Action != "emby_delete_failed" || l.Result != "failed" {
		t.Fatalf("日志内容不符: %+v", l)
	}
	if l.RemotePath != "/115/AV/T/T-038" {
		t.Fatalf("远端路径映射错误: %q", l.RemotePath)
	}
	if !strings.Contains(l.Detail, "超出允许删除范围") {
		t.Fatalf("detail 应含白名单错误: %q", l.Detail)
	}
}

// payload 超长时截断，避免撑爆数据库
func TestDBWebhookLogPayloadTruncate(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	big := strings.Repeat("x", 10_000)
	if err := database.InsertWebhookLog(db.WebhookLog{
		Kind: "emby", Event: "library.deleted", Payload: big,
		Action: "emby_delete", Result: "skipped",
	}); err != nil {
		t.Fatal(err)
	}
	logs, err := database.RecentWebhookLogs(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("期望 1 条日志，got %d", len(logs))
	}
	if len(logs[0].Payload) > 2051 { // 2048 + 省略号(3 字节)
		t.Fatalf("payload 未截断: len=%d", len(logs[0].Payload))
	}
}
