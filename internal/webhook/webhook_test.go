package webhook

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"smartstrm/internal/config"
	"smartstrm/internal/task"
)

func newTestRouter() (*gin.Engine, string) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Webhook: config.WebhookConfig{
			Token: "test-token-123",
		},
	}
	mgr := task.New(cfg, nil)
	h := New(cfg, mgr, nil)
	r := gin.New()
	h.Register(r)
	return r, "test-token-123"
}

// 合并后同一地址按请求体分派
func TestDispatchMergedWebhook(t *testing.T) {
	r, token := newTestRouter()

	// 1. Emby 通知格式 → 走删除同步（未启用返回 ignored）
	body := `{"Event":"item.deleted","Item":{"Path":"/strm/FC2/xxx.strm"}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/"+token, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "ignored: emby 删除同步未启用" {
		t.Fatalf("Emby 通知分派错误: code=%d body=%q", w.Code, w.Body.String())
	}

	// 2. 任务触发格式 → 走触发（触发为异步，返回 ok）
	body2 := `{"strmtask":"nonexist"}`
	req2 := httptest.NewRequest(http.MethodPost, "/webhook/"+token, bytes.NewBufferString(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("任务触发分派错误: code=%d body=%q", w2.Code, w2.Body.String())
	}

	// 2b. 缺 strmtask 字段 → 400
	req2b := httptest.NewRequest(http.MethodPost, "/webhook/"+token, bytes.NewBufferString(`{}`))
	w2b := httptest.NewRecorder()
	r.ServeHTTP(w2b, req2b)
	if w2b.Code != http.StatusBadRequest {
		t.Fatalf("缺字段未拒绝: code=%d body=%q", w2b.Code, w2b.Body.String())
	}

	// 3. 旧 Emby 地址仍兼容
	req3 := httptest.NewRequest(http.MethodPost, "/webhook/emby/"+token, bytes.NewBufferString(body))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("旧 Emby 地址失效: code=%d body=%q", w3.Code, w3.Body.String())
	}

	// 4. 无效 token 拒绝
	req4 := httptest.NewRequest(http.MethodPost, "/webhook/wrong-token", bytes.NewBufferString(body))
	w4 := httptest.NewRecorder()
	r.ServeHTTP(w4, req4)
	if w4.Code != http.StatusForbidden {
		t.Fatalf("无效 token 未拒绝: code=%d", w4.Code)
	}
}
