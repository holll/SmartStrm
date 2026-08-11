// Package webhook 实现任务触发与 Emby 删除同步
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"smartstrm/internal/config"
	"smartstrm/internal/db"
	"smartstrm/internal/driver"
	"smartstrm/internal/task"
)

// Handler Webhook 处理器
type Handler struct {
	cfg *config.Config
	mgr *task.Manager
	db  *db.DB
}

// New 创建 Webhook 处理器
func New(cfg *config.Config, mgr *task.Manager, database *db.DB) *Handler {
	return &Handler{cfg: cfg, mgr: mgr, db: database}
}

// audit 写入审计
func (h *Handler) audit(user, action, target, detail string) {
	if h.db != nil {
		_ = h.db.InsertAudit(user, action, target, detail)
	}
}

// Register 注册路由
func (h *Handler) Register(r *gin.Engine) {
	// 任务触发：POST /webhook/{token}，body 含 strmtask 字段
	r.POST("/webhook/:token", h.triggerHandler)
	// Emby 删除同步：POST /webhook/emby/{token}
	r.POST("/webhook/emby/:token", h.embyDeleteHandler)
}

// triggerReq 任务触发请求
type triggerReq struct {
	StrTask string `json:"strmtask"`
	Task    string `json:"task"`
}

// triggerHandler 外部工具（QAS/CloudSaver/脚本）转存后触发任务
func (h *Handler) triggerHandler(c *gin.Context) {
	if c.Param("token") != h.cfg.Webhook.Token {
		c.JSON(http.StatusForbidden, gin.H{"error": "无效的 token"})
		return
	}
	body, _ := io.ReadAll(c.Request.Body)
	var req triggerReq
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "JSON 解析失败"})
		return
	}
	tasks := req.StrTask
	if tasks == "" {
		tasks = req.Task
	}
	if tasks == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 strmtask 字段"})
		return
	}
	var failed []string
	for _, name := range strings.Split(tasks, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := h.mgr.Run(name); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
	}
	h.audit("webhook", "task_trigger", tasks, strings.Join(failed, ";"))
	if len(failed) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": strings.Join(failed, "; ")})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ============ Emby 删除同步 ============

// embyWebhook Emby 通知结构
type embyWebhook struct {
	Event string `json:"Event"`
	Item  struct {
		Path string `json:"Path"`
	} `json:"Item"`
}

// embyDeleteHandler Emby 删除通知 → 删除远端存储文件
func (h *Handler) embyDeleteHandler(c *gin.Context) {
	if c.Param("token") != h.cfg.Webhook.Token {
		c.JSON(http.StatusForbidden, gin.H{"error": "无效的 token"})
		return
	}
	es := h.cfg.Webhook.EmbyDeleteSync
	if !es.Enabled {
		c.String(http.StatusOK, "ignored: emby 删除同步未启用")
		return
	}

	body, _ := io.ReadAll(c.Request.Body)
	log.Printf("Emby webhook: %s", string(body))
	var wh embyWebhook
	if err := json.Unmarshal(body, &wh); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "JSON 解析失败"})
		return
	}

	switch wh.Event {
	case "item.deleted", "ItemDeleted", "library.deleted":
	default:
		c.String(http.StatusOK, "ignored")
		return
	}
	if wh.Item.Path == "" {
		c.String(http.StatusOK, "ok")
		return
	}

	if err := h.removeRemote(wh.Item.Path); err != nil {
		h.audit("webhook", "emby_delete_failed", wh.Item.Path, err.Error())
		log.Printf("Emby 删除同步失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.audit("webhook", "emby_delete", wh.Item.Path, "")
	c.String(http.StatusOK, "ok")
}

// removeRemote 将 Emby 中的 strm 路径映射为远端路径并删除
func (h *Handler) removeRemote(embyPath string) error {
	es := h.cfg.Webhook.EmbyDeleteSync

	// 1. 校验前缀：strm 目录在 Emby 中的路径
	if !strings.HasPrefix(embyPath, es.StrmInEmby) {
		return fmt.Errorf("非法路径，不在 strm 目录内: %s", embyPath)
	}
	relative := strings.TrimPrefix(embyPath, es.StrmInEmby)
	relative = strings.TrimPrefix(relative, "/")

	// 2. .strm 文件去掉文件名，统一按文件夹处理
	if strings.HasSuffix(strings.ToLower(relative), ".strm") {
		relative = filepath.Dir(relative)
	}

	// 3. 拼接存储内路径
	remoteDir := filepath.ToSlash(filepath.Join(es.StoragePathMap, relative))
	remoteDir = filepath.ToSlash(filepath.Clean(remoteDir))

	// 4. 白名单校验，防误删
	allowed := false
	for _, p := range es.AllowedPrefix {
		if strings.HasPrefix(remoteDir, p) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("超出允许删除范围: %s", remoteDir)
	}

	// 5. 调用存储驱动删除
	var storageCfg *config.Storage
	for i := range h.cfg.Storages {
		if h.cfg.Storages[i].Name == es.Storage {
			storageCfg = &h.cfg.Storages[i]
			break
		}
	}
	if storageCfg == nil {
		return fmt.Errorf("删除存储 %s 不存在", es.Storage)
	}
	drv, err := driver.New(storageCfg.Driver, storageCfg.URL, storageCfg.Token)
	if err != nil {
		return err
	}
	if err := drv.Remove(context.Background(), remoteDir); err != nil {
		return fmt.Errorf("删除 %s 失败: %w", remoteDir, err)
	}
	log.Printf("Emby 删除同步: %s -> %s", embyPath, remoteDir)
	return nil
}
