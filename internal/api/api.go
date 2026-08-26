// Package api 提供管理 REST API 与内置管理页面
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"smartstrm/internal/config"
	"smartstrm/internal/db"
	"smartstrm/internal/driver"
	"smartstrm/internal/organize"
	"smartstrm/internal/plugins"
	"smartstrm/internal/task"
	"smartstrm/internal/version"
)

// Server API 服务
type Server struct {
	cfg *config.Config
	mgr *task.Manager
	db  *db.DB

	mu     sync.Mutex
	tokens map[string]time.Time // token → 过期时间

	sseID    int64
	sseConns map[int64]sseConn // 活跃 SSE 日志流连接（登出/改密时强制断开）

	stateID    int64
	stateConns map[int64]stateConn // 活跃状态流连接（任务状态变化时广播）
	stateTimer *time.Timer         // 状态广播节流定时器

	orgMu      sync.Mutex // 目录整理防并发锁
	orgRunning bool       // 是否有整理正在后台执行
}

// sseConn 一条活跃的 SSE 日志流连接
type sseConn struct {
	cancel context.CancelFunc
	tok    string // 建立连接时使用的 token
}

// stateConn 一条活跃的状态流连接
type stateConn struct {
	ch     chan struct{} // 缓冲 1：待广播的帧，满时合并
	cancel context.CancelFunc
	tok    string // 建立连接时使用的 token
}

// New 创建 API 服务
func New(cfg *config.Config, mgr *task.Manager, database *db.DB) *Server {
	s := &Server{
		cfg:        cfg,
		mgr:        mgr,
		db:         database,
		tokens:     map[string]time.Time{},
		sseConns:   map[int64]sseConn{},
		stateConns: map[int64]stateConn{},
	}
	// 任务状态变化（开始/结束/panic 收尾）时广播给状态流订阅者
	mgr.SetOnState(s.onStateChanged)
	return s
}

// audit 写入审计日志
func (s *Server) audit(user, action, target, detail string) {
	if s.db != nil {
		_ = s.db.InsertAudit(user, action, target, detail)
	}
}

// Register 注册路由
func (s *Server) Register(r *gin.Engine) {
	r.POST("/api/login", s.login)
	r.POST("/api/logout", s.auth(), s.logout)
	r.POST("/api/password", s.auth(), s.changePassword)

	api := r.Group("/api", s.auth())
	{
		// 系统设置
		api.GET("/settings", s.getSettings)
		api.PUT("/settings", s.putSettings)
		// 存储
		api.GET("/storages", s.listStorages)
		api.POST("/storages", s.addStorage)
		api.PUT("/storages/:name", s.updateStorage)
		api.DELETE("/storages/:name", s.deleteStorage)
		// 存储浏览
		api.GET("/storages/:name/list", s.browseStorage)
		// 目录整理（cli 内部整理 / 移动到分类目录）
		api.POST("/organize", s.organize)
		// 任务
		api.GET("/tasks", s.listTasks)
		api.POST("/tasks", s.addTask)
		api.PUT("/tasks/:name", s.updateTask)
		api.DELETE("/tasks/:name", s.deleteTask)
		api.POST("/tasks/:name/run", s.runTask)
		api.POST("/tasks/:name/stop", s.stopTask)
		api.GET("/tasks/:name/log", s.taskLog)
		api.GET("/tasks/:name/log/stream", s.taskLogStream)
		api.POST("/tasks/run_all", s.runAll)
		api.GET("/tasks/status", s.taskStatus)
		api.POST("/tasks/:name/strm_replace", s.strmReplace)
		api.POST("/tasks/:name/overwrite", s.overwriteTask)
		api.POST("/tasks/:name/clear", s.clearTaskDir)
		// 插件
		api.GET("/plugins", s.listPlugins)
		api.PUT("/plugins/:id", s.updatePlugin)
		// Webhook 配置
		api.GET("/webhook/info", s.webhookInfo)
		api.PUT("/webhook", s.putWebhook)
		api.POST("/webhook/regenerate", s.regenerateWebhook)
		api.GET("/webhook/logs", s.webhookLogs)
	// 运行历史 / 审计
	api.GET("/runs", s.recentRuns)
	api.GET("/runs/:id/log", s.runLog)
	api.GET("/tasks/:name/history", s.taskHistory)
	api.GET("/audit", s.recentAudits)
	// 关于（版本 + 更新检查）
	api.GET("/about", s.about)
	// 任务状态推送（前端以此替代轮询）
	api.GET("/events/stream", s.stateStream)
	}
}

// about 关于信息：当前版本、构建信息、GitHub 最新版本检查结果
// ?refresh=1 忽略缓存强制重新检查
func (s *Server) about(c *gin.Context) {
	force := c.Query("refresh") == "1"
	info := version.Check(c.Request.Context(), force)
	c.JSON(http.StatusOK, info)
}

// ============ 任务状态推送（SSE） ============

// onStateChanged 任务状态变化回调（Manager 钩子）。
// 多次通知（RunAll 逐任务开始/结束）合并为 500ms 内一次广播
func (s *Server) onStateChanged() {
	s.mu.Lock()
	if s.stateTimer != nil {
		s.mu.Unlock()
		return // 已排定广播
	}
	s.stateTimer = time.AfterFunc(500*time.Millisecond, s.broadcastState)
	s.mu.Unlock()
}

// broadcastState 向全部状态流订阅者推送一帧（非阻塞，通道满时合并）
func (s *Server) broadcastState() {
	s.mu.Lock()
	s.stateTimer = nil
	for _, sc := range s.stateConns {
		select {
		case sc.ch <- struct{}{}:
		default:
		}
	}
	s.mu.Unlock()
}

// stateStream 任务状态 SSE 流：连接建立即推送一帧，此后任务状态变化时推送。
// 帧内不携带数据，前端收到后按需拉取可见页面的最新状态
func (s *Server) stateStream(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "不支持流式输出"})
		return
	}

	ch := make(chan struct{}, 1)
	sseCtx, cancel := context.WithCancel(c.Request.Context())
	s.mu.Lock()
	s.stateID++
	id := s.stateID
	s.stateConns[id] = stateConn{ch: ch, cancel: cancel, tok: strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.stateConns, id)
		s.mu.Unlock()
		cancel()
	}()

	// 连接建立即推送一帧，前端据此完成首次（重连后）刷新
	writeStateEvent(c.Writer)
	flusher.Flush()
	for {
		select {
		case <-sseCtx.Done():
			return
		case <-ch:
			writeStateEvent(c.Writer)
			flusher.Flush()
		}
	}
}

// writeStateEvent 写一帧空数据的状态事件
func writeStateEvent(w io.Writer) {
	_, _ = fmt.Fprintf(w, "event: state\ndata: {}\n\n")
}

// dropSSE 断开 SSE 连接（日志流与状态流）。tok 为空时断开全部（改密后旧会话失效），
// 否则仅断开该 token 的连接（登出时其他会话不受影响）
func (s *Server) dropSSE(tok string) {
	s.mu.Lock()
	var cancels []context.CancelFunc
	for id, sc := range s.sseConns {
		if tok == "" || sc.tok == tok {
			cancels = append(cancels, sc.cancel)
			delete(s.sseConns, id)
		}
	}
	for id, sc := range s.stateConns {
		if tok == "" || sc.tok == tok {
			cancels = append(cancels, sc.cancel)
			delete(s.stateConns, id)
		}
	}
	s.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// ============ 审计记录 ============

// recentAudits 最近审计记录
func (s *Server) recentAudits(c *gin.Context) {
	if s.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "数据库未启用"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit < 1 || limit > 500 {
		limit = 100
	}
	list, err := s.db.RecentAudits(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

// webhookLogs 最近 Webhook 处理日志（收到通知 / 执行动作 / 结果）
func (s *Server) webhookLogs(c *gin.Context) {
	if s.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "数据库未启用"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit < 1 || limit > 500 {
		limit = 100
	}
	list, err := s.db.RecentWebhookLogs(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

// ============ 运行历史 ============

// recentRuns 最近运行记录
func (s *Server) recentRuns(c *gin.Context) {
	if s.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "数据库未启用"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 || limit > 200 {
		limit = 50
	}
	list, err := s.db.RecentRuns(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

// runLog 某次运行的完整日志（从数据库读）
func (s *Server) runLog(c *gin.Context) {
	if s.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "数据库未启用"})
		return
	}
	id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	if id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的运行 ID"})
		return
	}
	lines, err := s.db.RunLogs(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, lines)
}

// taskHistory 任务运行历史
func (s *Server) taskHistory(c *gin.Context) {
	if s.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "数据库未启用"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit < 1 || limit > 200 {
		limit = 20
	}
	list, err := s.db.TaskRuns(c.Param("name"), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, list)
}

// ============ 认证 ============

func (s *Server) login(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	// 单用户，固定 admin（审计记录来源 IP，便于发现暴力尝试）
	clientIP := c.ClientIP()
	if req.Username != "" && req.Username != "admin" {
		s.audit(req.Username, "login_failed", clientIP, "")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号或密码错误"})
		return
	}
	if s.db == nil || !s.db.VerifyPassword(req.Password) {
		s.audit("admin", "login_failed", clientIP, "")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号或密码错误"})
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.tokens[token] = time.Now().Add(7 * 24 * time.Hour)
	s.mu.Unlock()
	s.audit("admin", "login", "", "")
	c.JSON(http.StatusOK, gin.H{"token": token})
}

// changePassword 修改密码（校验旧密码，成功后使全部 token 失效）
func (s *Server) changePassword(c *gin.Context) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if len(req.NewPassword) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码至少 6 位"})
		return
	}
	if s.db == nil || !s.db.VerifyPassword(req.OldPassword) {
		c.JSON(http.StatusForbidden, gin.H{"error": "旧密码错误"})
		return
	}
	if err := s.db.SetAdmin("admin", req.NewPassword); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.mu.Lock()
	s.tokens = map[string]time.Time{}
	s.mu.Unlock()
	// 密码已变更：断开全部 SSE 连接，旧会话立即失效
	s.dropSSE("")
	s.audit("admin", "password_change", "", "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) logout(c *gin.Context) {
	tok := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
	s.mu.Lock()
	delete(s.tokens, tok)
	s.mu.Unlock()
	// 断开该 token 的活跃 SSE 连接（其他会话不受影响）
	s.dropSSE(tok)
	s.audit("admin", "logout", "", "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// auth 认证中间件（顺带清理过期 token，防 map 无限增长）
func (s *Server) auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		now := time.Now()
		s.mu.Lock()
		if len(s.tokens) > 100 { // 低频全量清理，避免每次请求遍历
			for k, exp := range s.tokens {
				if now.After(exp) {
					delete(s.tokens, k)
				}
			}
		}
		exp, ok := s.tokens[tok]
		s.mu.Unlock()
		if !ok || now.After(exp) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未登录"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// ============ 系统设置 ============

func (s *Server) getSettings(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"strm":   s.cfg.STRM,
		"server": s.cfg.Server,
	})
}

func (s *Server) putSettings(c *gin.Context) {
	var req struct {
		STRM config.STRMConfig `json:"strm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	s.cfg.STRM = req.STRM
	if s.db != nil {
		if err := db.SetSettingJSON(s.db, "strm", req.STRM); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	s.audit("", "settings_update", "", "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ============ 存储 ============

func (s *Server) listStorages(c *gin.Context) {
	storages := s.cfg.Storages
	if storages == nil {
		storages = []config.Storage{}
	}
	c.JSON(http.StatusOK, storages)
}

func (s *Server) addStorage(c *gin.Context) {
	var st config.Storage
	if err := c.ShouldBindJSON(&st); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if st.Name == "" || st.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "存储名称与地址不能为空"})
		return
	}
	if st.Driver == "" {
		st.Driver = "openlist"
	}
	if st.Driver != "openlist" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "目前仅支持 openlist 驱动"})
		return
	}
	for i := range s.cfg.Storages {
		if s.cfg.Storages[i].Name == st.Name {
			c.JSON(http.StatusConflict, gin.H{"error": "存储名已存在"})
			return
		}
	}
	s.cfg.Storages = append(s.cfg.Storages, st)
	if s.db != nil {
		if err := s.db.SaveStorage(st); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	s.audit("", "storage_create", st.Name, st.URL)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) updateStorage(c *gin.Context) {
	name := c.Param("name")
	var st config.Storage
	if err := c.ShouldBindJSON(&st); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	st.Name = name
	for i := range s.cfg.Storages {
		if s.cfg.Storages[i].Name == name {
			s.cfg.Storages[i] = st
			if s.db != nil {
				if err := s.db.SaveStorage(st); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
			}
			s.audit("", "storage_update", name, st.URL)
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "存储不存在"})
}

func (s *Server) deleteStorage(c *gin.Context) {
	name := c.Param("name")
	for i := range s.cfg.Storages {
		if s.cfg.Storages[i].Name == name {
			s.cfg.Storages = append(s.cfg.Storages[:i], s.cfg.Storages[i+1:]...)
			if s.db != nil {
				if err := s.db.DeleteStorage(name); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
			}
			s.audit("", "storage_delete", name, "")
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "存储不存在"})
}

// browseStorage 存储浏览
func (s *Server) browseStorage(c *gin.Context) {
	name := c.Param("name")
	path := c.Query("path")
	if path == "" {
		path = "/"
	}
	st := findStorage(s.cfg, name)
	if st == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "存储不存在"})
		return
	}
	drv, err := driver.New(st.Driver, st.URL, st.Token)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	files, err := drv.List(c.Request.Context(), path)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, files)
}

// organizeReq 目录整理请求
type organizeReq struct {
	Storage   string `json:"storage"`
	Path      string `json:"path"`
	Mode      string `json:"mode"`     // organize / move / all
	IDMode    string `json:"id_mode"`  // AV / FC2
	DryRun    bool   `json:"dry_run"`  // true 仅预览计划
	Overwrite bool   `json:"overwrite"`
}

// organize 执行目录整理（识别番号→一步入库→移动遗留分类）。
// 响应以 SSE 流式推送：执行中推 progress 帧（大量文件时前端可显示进度），结束推 done / error 帧
func (s *Server) organize(c *gin.Context) {
	var req organizeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败"})
		return
	}
	if req.Storage == "" || req.Path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 storage 或 path"})
		return
	}
	st := findStorage(s.cfg, req.Storage)
	if st == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "存储不存在"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "不支持流式输出"})
		return
	}

	ctx := c.Request.Context()
	drv := driver.NewOpenList(st.URL, st.Token)

	s.orgMu.Lock()
	if s.orgRunning {
		s.orgMu.Unlock()
		io.WriteString(c.Writer, organizeEventFrame("error", gin.H{"error": "已有目录整理正在进行，请稍后再试"}))
		flusher.Flush()
		return
	}
	s.orgRunning = true
	s.orgMu.Unlock()

	// 整理执行使用后台 context，脱离 HTTP 请求生命周期：
	// 网页关闭（请求 context 取消）只停止 SSE 推送，不中断正在执行的移动/建目录/重命名。
	execCtx := context.Background()
	evCh := make(chan string, 512)
	go func() {
		// 无论正常完成还是异常退出，都释放防并发锁
		defer func() {
			s.orgMu.Lock()
			s.orgRunning = false
			s.orgMu.Unlock()
		}()
		defer close(evCh)
		res, err := organize.Run(execCtx, drv, organize.Options{
			TargetPath: req.Path,
			Mode:       req.Mode,
			IDMode:     req.IDMode,
			DryRun:     req.DryRun,
			Overwrite:  req.Overwrite,
			Progress: func(stage string, done, total int, op organize.MoveOp) {
				frame := organizeEventFrame("progress", gin.H{
					"stage": stage, "done": done, "total": total,
					"old": op.Old, "new": op.New,
				})
				select {
				case evCh <- frame:
				default:
				}
			},
		})
		if err != nil {
			evCh <- organizeEventFrame("error", gin.H{"error": err.Error()})
			return
		}
		evCh <- organizeEventFrame("done", gin.H{"plan": res.Plan, "errors": res.Errors})
		s.audit("", "organize", req.Storage+req.Path, fmt.Sprintf("mode=%s dry_run=%v 处理 %d 项", req.Mode, req.DryRun, len(res.Plan)))
	}()

	for {
		select {
		case frame, ok := <-evCh:
			if !ok {
				flusher.Flush()
				return
			}
			io.WriteString(c.Writer, frame)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// organizeEventFrame 生成一条 SSE 事件帧
func organizeEventFrame(event string, data any) string {
	b, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event, b)
}

// ============ 任务 ============

func (s *Server) listTasks(c *gin.Context) {
	type taskView struct {
		config.Task
		NextRun  *time.Time `json:"next_run,omitempty"`
		Running  bool       `json:"running"`
	}
	next := s.mgr.NextRuns()
	status := s.mgr.Status()
	out := make([]taskView, 0, len(s.cfg.Tasks))
	for i := range s.cfg.Tasks {
		t := s.cfg.Tasks[i]
		v := taskView{Task: t}
		if n, ok := next[t.Name]; ok {
			v.NextRun = &n
		}
		if st, ok := status[t.Name]; ok && st.Running {
			v.Running = true
		}
		out = append(out, v)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) addTask(c *gin.Context) {
	var t config.Task
	if err := c.ShouldBindJSON(&t); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if t.Name == "" || t.Storage == "" || t.StoragePath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务名、存储、路径不能为空"})
		return
	}
	for i := range s.cfg.Tasks {
		if s.cfg.Tasks[i].Name == t.Name {
			c.JSON(http.StatusConflict, gin.H{"error": "任务名已存在"})
			return
		}
	}
	s.cfg.Tasks = append(s.cfg.Tasks, t)
	if s.db != nil {
		if err := s.db.SaveTask(t); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	s.mgr.Reload()
	s.audit("", "task_create", t.Name, t.Storage+t.StoragePath)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) updateTask(c *gin.Context) {
	name := c.Param("name")
	var t config.Task
	if err := c.ShouldBindJSON(&t); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	t.Name = name
	for i := range s.cfg.Tasks {
		if s.cfg.Tasks[i].Name == name {
			s.cfg.Tasks[i] = t
			if s.db != nil {
				if err := s.db.SaveTask(t); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
			}
			s.mgr.Reload()
			s.audit("", "task_update", name, t.Storage+t.StoragePath)
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
}

func (s *Server) deleteTask(c *gin.Context) {
	name := c.Param("name")
	for i := range s.cfg.Tasks {
		if s.cfg.Tasks[i].Name == name {
			s.cfg.Tasks = append(s.cfg.Tasks[:i], s.cfg.Tasks[i+1:]...)
			if s.db != nil {
				if err := s.db.DeleteTask(name); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
					return
				}
			}
			s.mgr.Reload()
			s.audit("", "task_delete", name, "")
			c.JSON(http.StatusOK, gin.H{"ok": true})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
}

func (s *Server) runTask(c *gin.Context) {
	name := c.Param("name")
	if err := s.mgr.Run(name); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	s.audit("", "task_run", name, "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// stopTask 停止任务
func (s *Server) stopTask(c *gin.Context) {
	name := c.Param("name")
	if err := s.mgr.Stop(name); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	s.audit("", "task_stop", name, "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// taskLog 任务日志增量拉取：GET /api/tasks/:name/log?after=<seq>
func (s *Server) taskLog(c *gin.Context) {
	name := c.Param("name")
	after, _ := strconv.ParseInt(c.Query("after"), 10, 64)
	logbuf := s.mgr.Log(name)
	lines := logbuf.Since(after)
	c.JSON(http.StatusOK, gin.H{
		"after": logbuf.LastSeq(),
		"lines": lines,
	})
}

// taskLogStream 任务日志 SSE 流：GET /api/tasks/:name/log/stream?after=<seq>
// 先发送 [after, 快照] 历史，再实时推送增量行；连接断开自动注销订阅
func (s *Server) taskLogStream(c *gin.Context) {
	name := c.Param("name")
	after, _ := strconv.ParseInt(c.Query("after"), 10, 64)
	logbuf := s.mgr.Log(name)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "不支持流式输出"})
		return
	}

	ch, snapSeq, unsub := logbuf.Subscribe()
	defer unsub()

	// 注册活跃连接：修改密码/登出时立即断开，防止失效会话继续接收日志
	sseCtx, cancel := context.WithCancel(c.Request.Context())
	s.mu.Lock()
	s.sseID++
	id := s.sseID
	s.sseConns[id] = sseConn{cancel: cancel, tok: strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sseConns, id)
		s.mu.Unlock()
		cancel()
	}()

	// 初始同步：订阅时刻之前的存量日志
	for _, l := range logbuf.History(after, snapSeq) {
		writeSSELine(c.Writer, l)
	}
	flusher.Flush()

	// 实时推送：订阅后的增量行（订阅注册先于历史写入，无丢失无乱序）
	for {
		select {
		case <-sseCtx.Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			writeSSELine(c.Writer, line)
			flusher.Flush()
		}
	}
}

// writeSSELine 以 SSE 事件格式写一行日志
func writeSSELine(w io.Writer, l task.LogLine) {
	b, _ := json.Marshal(l)
	_, _ = fmt.Fprintf(w, "event: line\ndata: %s\n\n", b)
}

func (s *Server) runAll(c *gin.Context) {
	failed := s.mgr.RunAll()
	s.audit("", "task_run_all", "", strings.Join(failed, ";"))
	if len(failed) > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": strings.Join(failed, "; ")})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) taskStatus(c *gin.Context) {
	status := s.mgr.Status()
	next := s.mgr.NextRuns()
	type view struct {
		Running bool              `json:"running"`
		Start   time.Time         `json:"start"`
		End     time.Time         `json:"end"`
		Result  *json.RawMessage  `json:"result,omitempty"`
		Error   string            `json:"error,omitempty"`
		NextRun *time.Time        `json:"next_run,omitempty"`
	}
	out := map[string]view{}
	for name, st := range status {
		v := view{Running: st.Running, Start: st.Start, End: st.End, Error: st.Error}
		if st.Result != nil {
			b, _ := json.Marshal(st.Result)
			rm := json.RawMessage(b)
			v.Result = &rm
		}
		out[name] = v
	}
	for name, n := range next {
		if _, ok := out[name]; !ok {
			out[name] = view{NextRun: &n}
		} else {
			v := out[name]
			v.NextRun = &n
			out[name] = v
		}
	}
	c.JSON(http.StatusOK, out)
}

// strmReplace 批量替换任务已生成的 STRM 内容
func (s *Server) strmReplace(c *gin.Context) {
	name := c.Param("name")
	var req struct {
		FindText   string `json:"find_text"`
		ReplaceText string `json:"replace_text"`
		RegexMode  bool   `json:"regex_mode"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	var taskCfg *config.Task
	for i := range s.cfg.Tasks {
		if s.cfg.Tasks[i].Name == name {
			taskCfg = &s.cfg.Tasks[i]
			break
		}
	}
	if taskCfg == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	taskDir := filepath.Join(s.cfg.STRM.SaveDir, name)
	s.audit("", "strm_replace", name, req.FindText+" → "+req.ReplaceText)
	var regexErr error
	if req.RegexMode {
		if _, err := regexp.Compile(req.FindText); err != nil {
			regexErr = err
		}
	}
	if regexErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "正则表达式无效: " + regexErr.Error()})
		return
	}
	count := 0
	_ = filepath.Walk(taskDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".strm") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(data)
		if req.RegexMode {
			content = regexp.MustCompile(req.FindText).ReplaceAllString(content, req.ReplaceText)
		} else {
			content = strings.ReplaceAll(content, req.FindText, req.ReplaceText)
		}
		if content != string(data) { // 只统计实际发生替换的文件
			_ = os.WriteFile(path, []byte(content), 0o644)
			count++
		}
		return nil
	})
	c.JSON(http.StatusOK, gin.H{"ok": true, "count": count})
}

// overwriteTask 全量覆写：清空任务目录后重新生成（仿原版任务工具）
func (s *Server) overwriteTask(c *gin.Context) {
	name := c.Param("name")
	if !s.taskExists(name) {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	taskDir := filepath.Join(s.cfg.STRM.SaveDir, name)
	if err := os.RemoveAll(taskDir); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 清空目录时间缓存：全量覆写必须忽略"目录未变化"检查，强制重新扫描
	if s.db != nil {
		if err := s.db.DeleteDirCache(name); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if err := s.mgr.Run(name); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	s.audit("", "task_overwrite", name, "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// clearTaskDir 一键清除：删除任务目录下所有文件（仿原版任务工具）
func (s *Server) clearTaskDir(c *gin.Context) {
	name := c.Param("name")
	if !s.taskExists(name) {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	taskDir := filepath.Join(s.cfg.STRM.SaveDir, name)
	if err := os.RemoveAll(taskDir); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.audit("", "task_clear", name, "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// taskExists 任务是否存在
func (s *Server) taskExists(name string) bool {
	for i := range s.cfg.Tasks {
		if s.cfg.Tasks[i].Name == name {
			return true
		}
	}
	return false
}

// ============ 插件 ============

func (s *Server) listPlugins(c *gin.Context) {
	type pluginView struct {
		ID      string              `json:"id"`
		Name    string              `json:"name"`
		Version string              `json:"version"`
		Enabled bool                `json:"enabled"`
		Config  config.PluginConfig `json:"config"`
	}
	out := make([]pluginView, 0, len(plugins.List()))
	for _, p := range plugins.List() {
		cfg := s.cfg.Plugins[p.ID()]
		out = append(out, pluginView{
			ID:      p.ID(),
			Name:    p.Name(),
			Version: p.Version(),
			Enabled: p.Enabled(cfg),
			Config:  cfg,
		})
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) updatePlugin(c *gin.Context) {
	id := c.Param("id")
	if plugins.Get(id) == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "插件不存在"})
		return
	}
	var cfg config.PluginConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	if s.cfg.Plugins == nil {
		s.cfg.Plugins = map[string]config.PluginConfig{}
	}
	s.cfg.Plugins[id] = cfg
	if s.db != nil {
		if err := db.SetSettingJSON(s.db, "plugins", s.cfg.Plugins); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	s.audit("", "plugin_update", id, "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ============ Webhook 信息 ============

func (s *Server) webhookInfo(c *gin.Context) {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	host := c.Request.Host
	c.JSON(http.StatusOK, gin.H{
		"trigger":   scheme + "://" + host + "/webhook/" + s.cfg.Webhook.Token,
		"emby":      scheme + "://" + host + "/webhook/emby/" + s.cfg.Webhook.Token,
		"token":     s.cfg.Webhook.Token,
		"emby_sync": s.cfg.Webhook.EmbyDeleteSync,
	})
}

// putWebhook 保存 webhook 配置（Emby 删除同步设置）
func (s *Server) putWebhook(c *gin.Context) {
	var req struct {
		EmbyDeleteSync config.EmbyDeleteSync `json:"emby_delete_sync"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	s.cfg.Webhook.EmbyDeleteSync = req.EmbyDeleteSync
	if s.db != nil {
		if err := s.db.SaveWebhook(db.WebhookConfig{Token: s.cfg.Webhook.Token, EmbyDeleteSync: req.EmbyDeleteSync}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	s.audit("", "webhook_update", "", "")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// regenerateWebhook 重新生成 webhook token（旧地址立即失效）
func (s *Server) regenerateWebhook(c *gin.Context) {
	if s.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "数据库未启用"})
		return
	}
	tok, err := s.db.RegenerateWebhookToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	s.cfg.Webhook.Token = tok
	s.audit("", "webhook_regenerate", "", "")
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":      true,
		"token":   tok,
		"trigger": scheme + "://" + c.Request.Host + "/webhook/" + tok,
		"emby":    scheme + "://" + c.Request.Host + "/webhook/emby/" + tok,
	})
}

// findStorage 查找存储
func findStorage(cfg *config.Config, name string) *config.Storage {
	for i := range cfg.Storages {
		if cfg.Storages[i].Name == name {
			return &cfg.Storages[i]
		}
	}
	return nil
}
