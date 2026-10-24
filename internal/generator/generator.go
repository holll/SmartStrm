// Package generator 实现 STRM 生成核心：扫描远端 → 过滤 → 插件 → 生成 .strm
package generator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	snapshot := cfg.Snapshot()
	taskCopy := *task
	taskCopy.Plugins = config.ClonePlugins(task.Plugins)
	cfg = snapshot
	task = &taskCopy
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
	if err := config.ValidatePlugins(plugins.MergeConfig(g.cfg.Plugins, g.task.Plugins)); err != nil {
		g.result.Errors = append(g.result.Errors, err.Error())
		return g.result
	}

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

	safeDir, err := config.TaskDir(g.cfg.STRM.SaveDir, g.task.Name)
	if err != nil {
		g.result.Errors = append(g.result.Errors, err.Error())
		return g.result
	}
	g.taskDir = safeDir
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
	complete := g.walkDir(ctx, g.task.StoragePath, "", 0)
	complete = complete && ctx.Err() == nil && len(g.result.Errors) == 0

	if complete && !g.task.Incremental {
		g.logf("INFO", "同步模式：清理远端已不存在的本地文件")
		g.cleanupLocal(ctx)
	}
	complete = complete && ctx.Err() == nil && len(g.result.Errors) == 0
	if complete {
		g.saveDirCache()
	} else if g.db != nil {
		if err := g.db.DeleteDirCache(g.cacheKey()); err != nil {
			g.result.Errors = append(g.result.Errors, fmt.Sprintf("清除失败任务缓存失败: %v", err))
		}
	}
	return g.result
}

// walkDir 递归扫描远端目录并生成
// mtime 为该目录条目（来自父目录列表）的修改时间，0 表示根目录不记录
// 返回 false 表示目录未完整处理（列表失败/任务被停止/子目录失败），
// 此时不记录 mtime 缓存，下次运行仍会扫描，避免「目录已建但 strm 未生成」被跳过
func (g *Generator) walkDir(ctx context.Context, remotePath, localRel string, mtime int64) bool {
	if ctx.Err() != nil {
		return false
	}
	errorsBefore := len(g.result.Errors)
	g.logf("INFO", "读取目录: %s", remotePath)
	files, err := g.drv.List(ctx, remotePath)
	if err != nil {
		msg := fmt.Sprintf("读取目录失败: %s (%v)", remotePath, err)
		g.result.Errors = append(g.result.Errors, msg)
		g.result.ListFailed++
		g.logf("ERROR", "%s", msg)
		return false
	}
	if !g.delayList(ctx) {
		return false
	}

	localDir := g.taskDir
	if localRel != "" {
		localDir = filepath.Join(g.taskDir, filepath.FromSlash(localRel))
	}
	if localRel != "" {
		if err := safeLocal(g.taskDir, localDir); err != nil {
			g.result.Errors = append(g.result.Errors, err.Error())
			return false
		}
	}
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		g.result.Errors = append(g.result.Errors, err.Error())
		return false
	}

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			g.logf("WARN", "任务被停止，中断于 %s", remotePath)
			return false
		}

		if !safeComponent(f.Name) {
			g.result.Errors = append(g.result.Errors, "非法远端文件名: "+f.Name)
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
			if g.task.Incremental && g.task.DirTimeCheck && g.dirUnchanged(remotePath, f) {
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
			g.generateStrm(f, remotePath, rel, ext)
		case g.isCopyExt(ext):
			g.copyFile(ctx, f, remotePath, localDir)
		}
	}

	if ctx.Err() != nil || len(g.result.Errors) > errorsBefore {
		return false
	}
	// 目录完整处理成功后才记录 mtime，供下次目录时间检查
	if mtime > 0 {
		g.dirCache[remotePath] = mtime
	}
	return true
}

// generateStrm 为媒体文件生成 STRM
func (g *Generator) generateStrm(f driver.File, remotePath, rel, ext string) {
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
	content := g.buildContent(remoteFull)

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
	if !safeComponent(strmName) {
		g.result.Errors = append(g.result.Errors, "非法 STRM 文件名: "+strmName)
		return
	}
	localPath := filepath.Join(localDir, strmName)
	if err := safeLocal(g.taskDir, localPath); err != nil {
		g.result.Errors = append(g.result.Errors, err.Error())
		return
	}

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

// buildContent 构建 STRM 内容：{base}/d/{路径}（路径兼容模式，直链解析交给 OpenList）
func (g *Generator) buildContent(remoteFull string) string {
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
func (g *Generator) copyFile(ctx context.Context, f driver.File, remotePath, localDir string) {
	dst := filepath.Join(localDir, f.Name)
	if err := safeLocal(g.taskDir, dst); err != nil {
		g.result.Errors = append(g.result.Errors, err.Error())
		return
	}

	// 已存在且大小一致则跳过
	if st, err := os.Stat(dst); err == nil && st.Size() == f.Size {
		g.remoteSet[filepath.ToSlash(relFrom(g.taskDir, dst))] = true
		g.result.Skipped++
		return
	}

	base := g.drv.(interface{ Base() string }).Base()
	dlURL := strings.TrimSuffix(base, "/") + "/d" + escapePath(joinRemote(remotePath, f.Name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		g.result.Errors = append(g.result.Errors, err.Error())
		return
	}
	resp, err := g.client.Do(req)
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
	out, err := os.CreateTemp(localDir, ".smartstrm-copy-*")
	if err != nil {
		g.result.Errors = append(g.result.Errors, fmt.Sprintf("创建临时文件失败: %v", err))
		return
	}
	tmp := out.Name()
	if err := out.Chmod(0o644); err != nil {
		g.result.Errors = append(g.result.Errors, fmt.Sprintf("设置文件权限失败: %v", finishCopy(out, err, tmp, dst, os.Rename)))
		return
	}
	_, copyErr := io.Copy(out, resp.Body)
	if err := finishCopy(out, copyErr, tmp, dst, os.Rename); err != nil {
		g.result.Errors = append(g.result.Errors, fmt.Sprintf("复制 %s 失败: %v", dst, err))
		return
	}
	g.remoteSet[filepath.ToSlash(relFrom(g.taskDir, dst))] = true
	g.result.Copied++
	g.logf("INFO", "复制 %s → %s", joinRemote(remotePath, f.Name), dst)
	g.delayCopy(ctx)
}

// cleanupLocal 同步模式：删除远端已不存在的本地文件
func (g *Generator) cleanupLocal(ctx context.Context) {
	var localFiles []string
	err := filepath.Walk(g.taskDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
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
	if err != nil {
		g.result.Errors = append(g.result.Errors, fmt.Sprintf("枚举清理目录失败: %v", err))
		return
	}
	sort.Strings(localFiles)
	for _, p := range localFiles {
		if ctx.Err() != nil {
			return
		}
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
	cache, err := g.db.LoadDirCacheNamespace(g.task.Name, g.cacheKey())
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
	if err := g.db.SaveDirCacheNamespace(g.task.Name, g.cacheKey(), g.dirCache); err != nil {
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

func (g *Generator) delayList(ctx context.Context) bool {
	if g.env != nil {
		return waitContext(ctx, g.env.DelayAfter)
	}
	return ctx.Err() == nil
}

func (g *Generator) delayCopy(ctx context.Context) {
	if g.env == nil {
		return
	}
	for _, p := range plugins.List() {
		if d, ok := p.(plugins.Delay); ok && p.Enabled(g.env.PluginConfig(p)) {
			if t := d.CopyDelay(g.env); t > 0 {
				if !waitContext(ctx, t) {
					return
				}
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

func writeStrm(path, content string) error { return WriteSTRM(path, content) }

// WriteSTRM atomically publishes content after successful write and close.
func WriteSTRM(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := os.CreateTemp(filepath.Dir(path), ".smartstrm-strm-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	if err := out.Chmod(0o644); err != nil {
		return finishCopy(out, err, tmp, path, os.Rename)
	}
	_, err = io.WriteString(out, content)
	return finishCopy(out, err, tmp, path, os.Rename)
}

// escapePath URL 编码路径（保留 /）
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func waitContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}
func safeComponent(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00:")
}
func safeLocal(root, dst string) error {
	rel, err := filepath.Rel(root, dst)
	if err != nil {
		return err
	}
	_, err = config.TaskDir(root, filepath.ToSlash(rel))
	return err
}

// finishCopy never publishes a partially written or unsuccessfully closed file.
func finishCopy(out io.Closer, copyErr error, tmp, dst string, rename func(string, string) error) error {
	closeErr := out.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		copyErr = rename(tmp, dst)
	}
	if copyErr != nil {
		_ = os.Remove(tmp)
	}
	return copyErr
}

// A configuration-addressed namespace prevents in-flight old runs from overwriting
// the cache of a new configuration. Legacy unversioned caches are never trusted.
func (g *Generator) cacheKey() string {
	var storage config.Storage
	for _, st := range g.cfg.Storages {
		if st.Name == g.task.Storage {
			storage = st
			break
		}
	}
	data, _ := json.Marshal(struct {
		STRM    config.STRMConfig
		Task    *config.Task
		Plugins map[string]config.PluginConfig
		Storage config.Storage
	}{g.cfg.STRM, g.task, g.cfg.Plugins, storage})
	return fmt.Sprintf("%s\x00%x", g.task.Name, sha256.Sum256(data))
}
