// Package generator 实现 STRM 生成核心：扫描远端 → 过滤 → 插件 → 生成 .strm
package generator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"smartstrm/internal/config"
	"smartstrm/internal/db"
	"smartstrm/internal/driver"
	"smartstrm/internal/plugins"
)

// Result 一次任务运行的统计结果
type Result struct {
	Generated   int      `json:"generated"`    // 生成 STRM 数
	Copied      int      `json:"copied"`       // 复制文件数
	Removed     int      `json:"removed"`      // 同步模式清理数
	Skipped     int      `json:"skipped"`      // 跳过文件数
	SkippedDirs int      `json:"skipped_dirs"` // 跳过目录数
	ListFailed  int      `json:"list_failed"`  // 读取目录失败数
	Errors      []string `json:"errors"`
}

// Logger 任务日志接口
type Logger interface {
	Logf(level, format string, args ...any)
}

// Generator 单次任务生成器
type Generator struct {
	cfg     *config.Config
	task    *config.Task
	drv     driver.Driver
	db      *db.DB
	env     *plugins.Env
	taskDir string // save_dir/任务名
	result  *Result
	client  *http.Client
	logger  Logger

	// 同步模式：远端存在的本地文件集合（相对 taskDir 的路径）
	remoteSet map[string]bool
	// 目录时间检查缓存: remotePath → 上次扫描时的远端 mtime(unix)（持久化于 SQLite dir_cache 表）
	dirCache map[string]int64
}

// New 创建生成器
func New(cfg *config.Config, task *config.Task, drv driver.Driver, database *db.DB) *Generator {
	return &Generator{
		cfg:       cfg,
		task:      task,
		drv:       drv,
		db:        database,
		taskDir:   filepath.Join(cfg.STRM.SaveDir, task.Name),
		client:    &http.Client{Timeout: 60 * time.Second},
		remoteSet: map[string]bool{},
		dirCache:  map[string]int64{},
	}
}

// SetLogger 设置任务日志输出
func (g *Generator) SetLogger(l Logger) { g.logger = l }

func (g *Generator) logf(level, format string, args ...any) {
	if g.logger != nil {
		g.logger.Logf(level, format, args...)
	}
}

// Run 执行生成
func (g *Generator) Run(ctx context.Context) *Result {
	g.env = plugins.EnvForTask(ctx, g.cfg, g.task)
	g.result = &Result{}
	// 汇总与结束日志统一在 defer 输出，确保所有返回路径（含创建目录失败提前返回）都有统计行
	defer func() {
		g.logf("INFO", "【生成文件完成】 生成 %d 个，复制 %d 个，跳过 %d 个，跳过目录 %d 个，读取目录失败 %d 个",
			g.result.Generated, g.result.Copied, g.result.Skipped, g.result.SkippedDirs, g.result.ListFailed)
		if ctx.Err() != nil {
			g.logf("WARN", "任务被停止")
		} else {
			g.logf("INFO", "任务完成。")
		}
	}()

	if err := os.MkdirAll(g.taskDir, 0o755); err != nil {
		g.result.Errors = append(g.result.Errors, fmt.Sprintf("创建目录失败: %v", err))
		return g.result
	}
	g.loadDirCache()

	// 任务信息块（仿原版日志格式）
	yn := func(b bool) string {
		if b {
			return "True"
		}
		return "False"
	}
	g.logf("INFO", "==================================================")
	g.logf("INFO", "开始任务: %s", g.task.Name)
	g.logf("INFO", "任务类型: 生成 STRM")
	g.logf("INFO", "远端路径: %s", g.task.StoragePath)
	g.logf("INFO", "增量同步: %s", yn(g.task.Incremental))
	g.logf("INFO", "目录时间检查: %s", yn(g.task.DirTimeCheck))
	g.logf("INFO", "保存目录: %s", filepath.ToSlash(g.taskDir))
	g.logf("INFO", "==================================================")
	g.logf("INFO", ">>> 开始生成")
	g.walkDir(ctx, g.task.StoragePath, "", 0)

	if !g.task.Incremental {
		g.logf("INFO", "同步模式：清理远端已不存在的本地文件")
		g.cleanupLocal()
	}
	g.saveDirCache()
	return g.result
}

// walkDir 递归扫描远端目录并生成
// mtime 为该目录条目（来自父目录列表）的修改时间，0 表示根目录不记录
// 返回 false 表示目录未完整处理（列表失败/任务被停止/子目录失败），
// 此时不记录 mtime 缓存，下次运行仍会扫描，避免「目录已建但 strm 未生成」被跳过
func (g *Generator) walkDir(ctx context.Context, remotePath, localRel string, mtime int64) bool {
	g.logf("INFO", "读取目录: %s", remotePath)
	files, err := g.drv.List(ctx, remotePath)
	if err != nil {
		msg := fmt.Sprintf("读取目录失败: %s (%v)", remotePath, err)
		g.result.Errors = append(g.result.Errors, msg)
		g.result.ListFailed++
		g.logf("ERROR", "%s", msg)
		return false
	}
	g.delayList()

	localDir := g.taskDir
	if localRel != "" {
		localDir = filepath.Join(g.taskDir, filepath.FromSlash(localRel))
	}
	_ = os.MkdirAll(localDir, 0o755)

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			g.logf("WARN", "任务被停止，中断于 %s", remotePath)
			return false
		}

		// 插件关键词过滤（文件计跳过，目录计跳过目录）
		if g.skipByPlugins(f.Name, f.IsDir) {
			g.logf("DEBUG", "关键词过滤跳过: %s/%s", remotePath, f.Name)
			if f.IsDir {
				g.result.SkippedDirs++
			} else {
				g.result.Skipped++
			}
			continue
		}

		rel := f.Name
		if localRel != "" {
			rel = filepath.ToSlash(filepath.Join(localRel, f.Name))
		}

		if f.IsDir {
			// 目录时间检查：远端目录未更新则跳过递归
			if g.task.DirTimeCheck && g.dirUnchanged(remotePath, f) {
				g.logf("DEBUG", "目录未变化跳过: %s/%s", remotePath, f.Name)
				g.result.SkippedDirs++
				continue
			}
			if !g.walkDir(ctx, joinRemote(remotePath, f.Name), rel, f.Modified.Unix()) {
				return false
			}
			continue
		}

		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f.Name), "."))
		switch {
		case g.isMediaExt(ext):
			g.generateStrm(ctx, f, remotePath, rel, ext)
		case g.isCopyExt(ext):
			g.copyFile(f, remotePath, localDir)
		}
	}

	// 目录完整处理成功后才记录 mtime，供下次目录时间检查
	if mtime > 0 {
		g.dirCache[remotePath] = mtime
	}
	return true
}

// generateStrm 为媒体文件生成 STRM
func (g *Generator) generateStrm(ctx context.Context, f driver.File, remotePath, rel, ext string) {
	// 大小阈值
	if g.env.MediaSize > 0 && f.Size < int64(g.env.MediaSize)*1024*1024 {
		g.result.Skipped++
		return
	}

	name := strings.TrimSuffix(f.Name, filepath.Ext(f.Name))
	strmName := fmt.Sprintf("%s.(%s).strm", name, ext)
	// 自定义文件名插件
	if g.env != nil {
		for _, p := range plugins.List() {
			if nf, ok := p.(plugins.NameFormatter); ok && p.Enabled(g.env.PluginConfig(p)) {
				if s := nf.FormatName(g.env, name, ext); s != "" {
					strmName = s
				}
			}
		}
	}

	remoteFull := joinRemote(remotePath, f.Name)
	content := g.buildContent(ctx, remoteFull, ext)

	// 内容替换插件
	if g.env != nil {
		for _, p := range plugins.List() {
			if cm, ok := p.(plugins.ContentModifier); ok && p.Enabled(g.env.PluginConfig(p)) {
				content = cm.ModifyContent(g.env, content)
			}
		}
	}

	// 本地路径：远端相对目录 + STRM 文件名
	localDir := filepath.Dir(filepath.Join(g.taskDir, filepath.FromSlash(rel)))
	localPath := filepath.Join(localDir, strmName)

	if err := writeStrm(localPath, content); err != nil {
		msg := fmt.Sprintf("写入 %s 失败: %v", localPath, err)
		g.result.Errors = append(g.result.Errors, msg)
		g.logf("ERROR", "%s", msg)
		return
	}
	g.remoteSet[filepath.ToSlash(relFrom(g.taskDir, localPath))] = true
	g.result.Generated++
	g.logf("INFO", "生成 %s → %s", remoteFull, localPath)
}

// buildContent 构建 STRM 内容
// path 模式: {base}/d/{路径}（OpenList 直链下载）
// fid 模式: 调用驱动 GetDirectLink 获取直链
func (g *Generator) buildContent(ctx context.Context, remoteFull, ext string) string {
	if g.cfg.STRM.GenType == "fid" {
		if link, err := g.drv.GetDirectLink(ctx, remoteFull); err == nil && link != "" {
			return link
		}
		// 获取失败回退到路径模式
	}
	base := g.cfg.STRM.StrmBase
	if base == "" {
		base = g.drv.(interface{ Base() string }).Base()
	}
	base = strings.TrimSuffix(base, "/")
	if g.cfg.STRM.URLEncode {
		return base + "/d" + escapePath(remoteFull)
	}
	return base + "/d" + remoteFull
}

// copyFile 复制刮削文件到本地
func (g *Generator) copyFile(f driver.File, remotePath, localDir string) {
	dst := filepath.Join(localDir, f.Name)

	// 已存在且大小一致则跳过
	if st, err := os.Stat(dst); err == nil && st.Size() == f.Size {
		g.remoteSet[filepath.ToSlash(relFrom(g.taskDir, dst))] = true
		g.result.Skipped++
		return
	}

	base := g.drv.(interface{ Base() string }).Base()
	dlURL := strings.TrimSuffix(base, "/") + "/d" + escapePath(joinRemote(remotePath, f.Name))
	resp, err := g.client.Get(dlURL)
	if err != nil {
		msg := fmt.Sprintf("下载 %s 失败: %v", f.Name, err)
		g.result.Errors = append(g.result.Errors, msg)
		g.logf("ERROR", "%s", msg)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("下载 %s: HTTP %d", f.Name, resp.StatusCode)
		g.result.Errors = append(g.result.Errors, msg)
		g.logf("ERROR", "%s", msg)
		return
	}
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		msg := fmt.Sprintf("创建 %s 失败: %v", tmp, err)
		g.result.Errors = append(g.result.Errors, msg)
		g.logf("ERROR", "%s", msg)
		return
	}
	_, err = io.Copy(out, resp.Body)
	out.Close()
	if err != nil {
		os.Remove(tmp)
		msg := fmt.Sprintf("写入 %s 失败: %v", tmp, err)
		g.result.Errors = append(g.result.Errors, msg)
		g.logf("ERROR", "%s", msg)
		return
	}
	_ = os.Rename(tmp, dst)
	g.remoteSet[filepath.ToSlash(relFrom(g.taskDir, dst))] = true
	g.result.Copied++
	g.logf("INFO", "复制 %s → %s", joinRemote(remotePath, f.Name), dst)
	g.delayCopy()
}

// cleanupLocal 同步模式：删除远端已不存在的本地文件
func (g *Generator) cleanupLocal() {
	var localFiles []string
	_ = filepath.Walk(g.taskDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel := relFrom(g.taskDir, path)
		if g.task.KeepLocalAst && !strings.HasSuffix(rel, ".strm") {
			return nil // 保留本地刮削文件
		}
		if !g.remoteSet[filepath.ToSlash(rel)] {
			localFiles = append(localFiles, path)
		}
		return nil
	})
	sort.Strings(localFiles)
	for _, p := range localFiles {
		if err := os.Remove(p); err == nil {
			g.result.Removed++
			g.logf("INFO", "清理（远端已不存在）: %s", p)
		} else {
			msg := fmt.Sprintf("清理 %s 失败: %v", p, err)
			g.result.Errors = append(g.result.Errors, msg)
			g.logf("ERROR", "%s", msg)
		}
	}
}

// ============ 目录时间检查（缓存持久化于 SQLite） ============

func (g *Generator) loadDirCache() {
	if g.db == nil {
		return
	}
	cache, err := g.db.LoadDirCache(g.task.Name)
	if err != nil {
		g.logf("WARN", "读取目录时间缓存失败: %v", err)
		return
	}
	g.dirCache = cache
}

func (g *Generator) saveDirCache() {
	if g.db == nil {
		return
	}
	if err := g.db.SaveDirCache(g.task.Name, g.dirCache); err != nil {
		g.logf("WARN", "保存目录时间缓存失败: %v", err)
	}
}

// dirUnchanged 远端目录 mtime 未超过本地缓存则视为未变化
// f 为该目录条目（来自父目录列表），remotePath 为父目录
func (g *Generator) dirUnchanged(parentRemote string, f driver.File) bool {
	if f.Modified.IsZero() {
		return false // mtime 未知，不能跳过
	}
	full := joinRemote(parentRemote, f.Name)
	last, ok := g.dirCache[full]
	if !ok {
		return false // 首次见到，需扫描
	}
	return f.Modified.Unix() <= last
}

// ============ 工具函数 ============

// skipByPlugins 应用关键词过滤插件
func (g *Generator) skipByPlugins(name string, isDir bool) bool {
	if g.env == nil {
		return false
	}
	for _, p := range plugins.List() {
		if flt, ok := p.(plugins.Filter); ok && p.Enabled(g.env.PluginConfig(p)) {
			if flt.ShouldSkip(g.env, name, isDir) {
				return true
			}
		}
	}
	return false
}

func (g *Generator) isMediaExt(ext string) bool {
	for _, e := range g.env.MediaExt {
		if e == ext {
			return true
		}
	}
	return false
}

func (g *Generator) isCopyExt(ext string) bool {
	for _, e := range g.env.CopyExt {
		if e == ext {
			return true
		}
	}
	return false
}

func (g *Generator) delayList() {
	if g.env != nil && g.env.DelayAfter > 0 {
		time.Sleep(g.env.DelayAfter)
	}
}

func (g *Generator) delayCopy() {
	if g.env == nil {
		return
	}
	for _, p := range plugins.List() {
		if d, ok := p.(plugins.Delay); ok && p.Enabled(g.env.PluginConfig(p)) {
			if t := d.CopyDelay(g.env); t > 0 {
				time.Sleep(t)
			}
		}
	}
}

func joinRemote(dir, name string) string {
	if dir == "" || dir == "/" {
		return "/" + name
	}
	return strings.TrimSuffix(dir, "/") + "/" + name
}

func relFrom(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func writeStrm(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// escapePath URL 编码路径（保留 /）
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
