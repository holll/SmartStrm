// Package plugins 实现 STRM 生成过程中的插件系统
package plugins

import (
	"context"
	"time"

	"smartstrm/internal/config"
)

// Env 插件执行上下文
type Env struct {
	Ctx     context.Context
	Config  *config.Config
	Task    *config.Task
	SaveDir string // 任务生成目录
	// Scan 生效的扫描过滤（已被 scan_filter 插件覆盖后）
	MediaExt   []string
	MediaSize  int
	CopyExt    []string
	DelayAfter time.Duration // 目录列出后延时（task_delay 提供）

	merged map[string]config.PluginConfig // 任务级覆盖全局后的插件配置
}

// PluginConfig 返回插件合并后的配置
func (e *Env) PluginConfig(p Plugin) config.PluginConfig {
	if e.merged == nil {
		e.merged = MergeConfig(e.Config.Plugins, e.Task.Plugins)
	}
	if c, ok := e.merged[p.ID()]; ok {
		return c
	}
	return nil
}

// Plugin 插件基本接口
type Plugin interface {
	ID() string
	Name() string
	Version() string
	// Enabled 判断插件是否启用（合并任务级配置后）
	Enabled(cfg config.PluginConfig) bool
}

// Filter 文件/目录过滤钩子（skip_keyword）
type Filter interface {
	// ShouldSkip 返回 true 表示跳过该条目
	ShouldSkip(env *Env, name string, isDir bool) bool
}

// ContentModifier 内容修改钩子（content_replace）
type ContentModifier interface {
	// ModifyContent 修改生成的 STRM 内容
	ModifyContent(env *Env, content string) string
}

// NameFormatter 文件名格式化钩子（custom_strm_name）
type NameFormatter interface {
	// FormatName 返回 STRM 文件名（含 .strm 后缀），ext 为原媒体扩展名（不含点）
	FormatName(env *Env, name, ext string) string
}

// Delay 请求延时钩子（task_delay）
type Delay interface {
	// ListDelay 目录列出后延时
	ListDelay(env *Env) time.Duration
	// CopyDelay 文件复制后延时
	CopyDelay(env *Env) time.Duration
}

// ScanOverride 扫描设置覆盖钩子（scan_filter）
type ScanOverride interface {
	// Override 覆盖扫描设置，返回有效值
	Override(env *Env, mediaExt []string, mediaSize int, copyExt []string) ([]string, int, []string)
}

var registry = []Plugin{
	&ContentReplace{},
	&CustomStrmName{},
	&ScanFilter{},
	&SkipKeyword{},
	&TaskDelay{},
}

// List 返回全部插件
func List() []Plugin { return registry }

// Get 按 ID 查找插件
func Get(id string) Plugin {
	for _, p := range registry {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

// MergeConfig 合并插件配置：任务级覆盖全局
func MergeConfig(global, task map[string]config.PluginConfig) map[string]config.PluginConfig {
	merged := map[string]config.PluginConfig{}
	for k, v := range global {
		merged[k] = v
	}
	for k, v := range task {
		merged[k] = v
	}
	return merged
}

// getBool 从配置读取布尔值
func getBool(cfg config.PluginConfig, key string, def bool) bool {
	if v, ok := cfg[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// getString 从配置读取字符串值
func getString(cfg config.PluginConfig, key, def string) string {
	if v, ok := cfg[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// getInt 从配置读取整数值
func getInt(cfg config.PluginConfig, key string, def int) int {
	switch v := cfg[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return def
}

// EnvForTask 构建任务执行环境
func EnvForTask(ctx context.Context, cfg *config.Config, task *config.Task) *Env {
	env := &Env{
		Ctx:       ctx,
		Config:    cfg,
		Task:      task,
		SaveDir:   cfg.STRM.SaveDir,
		MediaExt:  cfg.STRM.MediaExt,
		MediaSize: cfg.STRM.MediaSize,
		CopyExt:   cfg.STRM.CopyExt,
	}
	// 应用 scan_filter 覆盖
	for _, p := range registry {
		if so, ok := p.(ScanOverride); ok {
			if p.Enabled(env.PluginConfig(p)) {
				env.MediaExt, env.MediaSize, env.CopyExt = so.Override(env, env.MediaExt, env.MediaSize, env.CopyExt)
			}
		}
	}
	// 应用 task_delay
	for _, p := range registry {
		if d, ok := p.(Delay); ok {
			if p.Enabled(env.PluginConfig(p)) {
				env.DelayAfter = d.ListDelay(env)
			}
		}
	}
	return env
}
