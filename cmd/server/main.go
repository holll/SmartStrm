// SmartStrm-Go: STRM 生成工具（无配置文件，全部配置由命令行参数 + 数据库管理）
package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"

	"smartstrm/internal/api"
	"smartstrm/internal/config"
	"smartstrm/internal/db"
	"smartstrm/internal/task"
	"smartstrm/internal/version"
	"smartstrm/internal/webhook"
)

//go:embed web
var webFS embed.FS

// 版本信息（由 build.bat 通过 -ldflags 注入，init 中转写至 internal/version 供 API 使用）
var (
	Version   = "0.0.0"
	Commit    = ""
	BuildTime = ""
)

func init() {
	version.Version = Version
	version.Commit = Commit
	version.BuildTime = BuildTime
}

func main() {
	// 内存优化：软上限 96MiB + 降低 GC 频率。
	// 无负载时 Go 运行时在 GC 后归还大部分堆，RSS 可降至 ~20MB；
	// 任务扫描峰值（日志缓冲/请求处理）在 96MiB 内绰绰有余
	debug.SetMemoryLimit(96 << 20)
	debug.SetGCPercent(200)

	port := flag.Int("port", 8024, "监听端口，如 8024")
	resetPwd := flag.Bool("reset-password", false, "重置 admin 密码：生成随机密码并打印，执行后退出")
	flag.Parse()

	// 数据库固定路径 data/data.db（自动创建目录）
	const dbPath = "data/data.db"
	if err := os.MkdirAll("data", 0o755); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
	}

	// 打开数据库（自动建表）
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer database.Close()
	log.Printf("数据库: %s", dbPath)
	log.Printf("SmartStrm-Go %s (commit %s, build %s)", Version, Commit, BuildTime)

	// 密码重置模式：生成随机密码，执行后退出
	if *resetPwd {
		pwd := db.RandomPassword()
		if err := database.SetAdmin("admin", pwd); err != nil {
			log.Fatalf("重置密码失败: %v", err)
		}
		fmt.Printf("admin 新密码: %s\n", pwd)
		fmt.Printf("（已 bcrypt 加密存储，请登录后尽快在 Web 后台修改）\n")
		return
	}

	// 首次启动：无账号则随机生成密码并打印
	if err := initAdmin(database); err != nil {
		log.Fatalf("初始化账号失败: %v", err)
	}

	// 清理上次异常退出遗留的"运行中"记录（程序被强制终止时任务无法收尾）
	if err := database.CleanupStaleRuns(); err != nil {
		log.Printf("清理遗留运行记录失败: %v", err)
	}

	// 从数据库组装运行时配置
	cfg := buildConfig(database, *port)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	// 静态资源 gzip 压缩（HTML/CSS/JS 文本压缩率 70-80%）；
	// SSE 日志流为实时流式输出，排除压缩
	r.Use(gzip.Gzip(gzip.DefaultCompression,
		gzip.WithExcludedPathsRegexs([]string{`/api/tasks/[^/]+/log/stream`})))

	mgr := task.New(cfg, database)
	mgr.Start()

	apiSrv := api.New(cfg, mgr, database)
	apiSrv.Register(r)

	wh := webhook.New(cfg, mgr, database)
	wh.Register(r)

	// 内置管理页面
	registerWeb(r, webFS)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("SmartStrm-Go 启动，监听 %s（管理页 http://localhost%s）", addr, addr)
	log.Printf("Webhook 触发地址: /webhook/%s", cfg.Webhook.Token)

	// 优雅关机：收到 SIGINT/SIGTERM 后停止接收新请求，等待当前请求完成
	srv := &http.Server{Addr: addr, Handler: r}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("服务启动失败: %v", err)
		}
	}()
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("收到退出信号，正在关闭…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭超时: %v", err)
	}
	log.Println("已退出")
}

// initAdmin 账号初始化（固定 admin，仅首次创建）
func initAdmin(database *db.DB) error {
	_, ok, err := database.LoadAdmin()
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	pwd := db.RandomPassword()
	if err := database.SetAdmin("admin", pwd); err != nil {
		return err
	}
	log.Printf("已生成初始账号: admin / %s（请尽快登录后在 Web 后台修改密码）", pwd)
	return nil
}

// buildConfig 从数据库组装运行时配置
func buildConfig(database *db.DB, port int) *config.Config {
	cfg := &config.Config{}

	// 账号固定 admin
	cfg.Server.Port = port
	cfg.Server.Username = "admin"

	// STRM 设置：无记录时写入默认值并持久化
	strmRaw, has, err := database.GetSetting("strm")
	if err != nil {
		log.Fatalf("读取 STRM 设置失败: %v", err)
	}
	if !has {
		cfg.STRM = config.DefaultSTRM()
		if err := db.SetSettingJSON(database, "strm", cfg.STRM); err != nil {
			log.Fatalf("写入默认 STRM 设置失败: %v", err)
		}
		log.Printf("已写入默认 STRM 设置")
	} else if err := json.Unmarshal([]byte(strmRaw), &cfg.STRM); err != nil {
		log.Fatalf("解析 STRM 设置失败: %v", err)
	}

	// 插件全局配置：无记录时写入空默认
	pluginsRaw, has, err := database.GetSetting("plugins")
	if err != nil {
		log.Fatalf("读取插件配置失败: %v", err)
	}
	if !has {
		cfg.Plugins = config.DefaultPlugins()
		if err := db.SetSettingJSON(database, "plugins", cfg.Plugins); err != nil {
			log.Fatalf("写入默认插件配置失败: %v", err)
		}
	} else if err := json.Unmarshal([]byte(pluginsRaw), &cfg.Plugins); err != nil {
		log.Fatalf("解析插件配置失败: %v", err)
	}
	if cfg.Plugins == nil {
		cfg.Plugins = config.DefaultPlugins()
	}

	// 存储 / 任务
	if cfg.Storages, err = database.ListStorages(); err != nil {
		log.Fatalf("读取存储配置失败: %v", err)
	}
	if cfg.Tasks, err = database.ListTasks(); err != nil {
		log.Fatalf("读取任务配置失败: %v", err)
	}

	// Webhook（无记录时生成随机 token 并持久化）
	wh, ok, err := database.LoadWebhook()
	if err != nil {
		log.Fatalf("读取 webhook 配置失败: %v", err)
	}
	if !ok {
		tok, err := database.RegenerateWebhookToken()
		if err != nil {
			log.Fatalf("生成 webhook token 失败: %v", err)
		}
		wh.Token = tok
	}
	cfg.Webhook.Token = wh.Token
	cfg.Webhook.EmbyDeleteSync = wh.EmbyDeleteSync

	log.Printf("已加载：存储 %d 个、任务 %d 个", len(cfg.Storages), len(cfg.Tasks))
	return cfg
}

// registerWeb 注册内置管理页面（递归注册静态文件 + NoRoute 兜底返回 index.html）
func registerWeb(r *gin.Engine, webFS embed.FS) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("读取内置页面失败: %v", err)
	}
	var walk func(dir fs.ReadDirFS, prefix string) error
	walk = func(dir fs.ReadDirFS, prefix string) error {
		entries, err := fs.ReadDir(dir, ".")
		if err != nil {
			return err
		}
		for _, e := range entries {
			rel := e.Name()
			if prefix != "" {
				rel = prefix + "/" + e.Name()
			}
			if e.IsDir() {
				d, err := fs.Sub(dir, e.Name())
				if err != nil {
					continue
				}
				_ = walk(d.(fs.ReadDirFS), rel)
				continue
			}
			data, err := fs.ReadFile(sub, rel)
			if err != nil {
				continue
			}
			fileData := data
			route := "/" + rel
			// ETag 基于文件内容：浏览器带 If-None-Match 时返回 304，
			// 内容未变不重复下载（升级后内容变化自然失效，无需手动清缓存）
			etag := fmt.Sprintf(`"%x"`, sha256.Sum256(fileData))
			r.GET(route, func(c *gin.Context) {
				c.Header("Cache-Control", "no-cache")
				c.Header("ETag", etag)
				if c.GetHeader("If-None-Match") == etag {
					c.Status(http.StatusNotModified)
					return
				}
				c.Data(http.StatusOK, contentType(rel), fileData)
			})
		}
		return nil
	}
	_ = walk(sub.(fs.ReadDirFS), "")
	// 其他非 API 路径返回管理页（SPA 兜底）
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api") || strings.HasPrefix(c.Request.URL.Path, "/webhook") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		indexData, _ := fs.ReadFile(sub, "index.html")
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexData)
	})
}

func contentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "application/javascript"
	case strings.HasSuffix(name, ".css"):
		return "text/css"
	case strings.HasSuffix(name, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(name, ".woff"):
		return "font/woff"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
