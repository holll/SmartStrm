package plugins

import (
	"regexp"
	"strings"
	"time"

	"smartstrm/internal/config"
)

// ============ STRM内容替换 ============

// ContentReplace 生成时替换 STRM 内的文本内容，支持正则
type ContentReplace struct{}

func (p *ContentReplace) ID() string                { return "content_replace" }
func (p *ContentReplace) Name() string              { return "STRM内容替换" }
func (p *ContentReplace) Version() string           { return "0.1" }
func (p *ContentReplace) Enabled(cfg config.PluginConfig) bool {
	return cfg != nil && getBool(cfg, "enabled", false)
}

// ModifyContent 替换 STRM 内容
func (p *ContentReplace) ModifyContent(env *Env, content string) string {
	cfg := env.PluginConfig(p)
	if cfg == nil || !p.Enabled(cfg) {
		return content
	}
	find := getString(cfg, "find_text", "")
	if find == "" {
		return content
	}
	replace := getString(cfg, "replace_text", "")
	if getBool(cfg, "regex_mode", false) {
		if re, err := regexp.Compile(find); err == nil {
			return re.ReplaceAllString(content, replace)
		}
		return content
	}
	return strings.ReplaceAll(content, find, replace)
}

// ============ 自定义STRM文件名 ============

// CustomStrmName 自定义 STRM 文件名，如 {name}.strm 或 {name}.({ext}).strm
type CustomStrmName struct{}

func (p *CustomStrmName) ID() string      { return "custom_strm_name" }
func (p *CustomStrmName) Name() string    { return "自定义STRM文件名" }
func (p *CustomStrmName) Version() string { return "0.1" }
func (p *CustomStrmName) Enabled(cfg config.PluginConfig) bool {
	return cfg != nil && getBool(cfg, "enabled", false)
}

// FormatName 变量：{name}=文件名部分 {ext}=原扩展名（不含点）
func (p *CustomStrmName) FormatName(env *Env, name, ext string) string {
	cfg := env.PluginConfig(p)
	if cfg == nil || !p.Enabled(cfg) {
		return ""
	}
	tmpl := getString(cfg, "custom_name", "{name}.({ext}).strm")
	tmpl = strings.ReplaceAll(tmpl, "{name}", name)
	tmpl = strings.ReplaceAll(tmpl, "{ext}", ext)
	if !strings.HasSuffix(strings.ToLower(tmpl), ".strm") {
		tmpl += ".strm"
	}
	return tmpl
}

// ============ 高级扫描过滤设置 ============

// ScanFilter 覆盖全局扫描设置：媒体/复制后缀、大小范围
type ScanFilter struct{}

func (p *ScanFilter) ID() string      { return "scan_filter" }
func (p *ScanFilter) Name() string    { return "高级扫描过滤设置" }
func (p *ScanFilter) Version() string { return "0.1" }
func (p *ScanFilter) Enabled(cfg config.PluginConfig) bool {
	return cfg != nil && getBool(cfg, "enabled", false)
}

// Override 覆盖扫描设置，留空字段沿用原值
func (p *ScanFilter) Override(env *Env, mediaExt []string, mediaSize int, copyExt []string) ([]string, int, []string) {
	cfg := env.PluginConfig(p)
	if cfg == nil {
		return mediaExt, mediaSize, copyExt
	}
	if s := getString(cfg, "media_ext", ""); s != "" {
		mediaExt = splitExt(s)
	}
	if v := getInt(cfg, "media_size_min", 0); v > 0 {
		mediaSize = v
	}
	if s := getString(cfg, "copy_ext", ""); s != "" {
		copyExt = splitExt(s)
	}
	return mediaExt, mediaSize, copyExt
}

// splitExt 逗号分隔后缀转列表
func splitExt(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, strings.ToLower(strings.TrimPrefix(e, ".")))
		}
	}
	return out
}

// ============ 文件名关键词过滤 ============

// SkipKeyword 过滤文件名含特定关键词的文件，支持正则与筛选反转
type SkipKeyword struct{}

func (p *SkipKeyword) ID() string      { return "skip_keyword" }
func (p *SkipKeyword) Name() string    { return "文件名关键词过滤" }
func (p *SkipKeyword) Version() string { return "0.2" }
func (p *SkipKeyword) Enabled(cfg config.PluginConfig) bool {
	return cfg != nil && getBool(cfg, "enabled", false)
}

// ShouldSkip 过滤逻辑
func (p *SkipKeyword) ShouldSkip(env *Env, name string, isDir bool) bool {
	cfg := env.PluginConfig(p)
	if cfg == nil || !p.Enabled(cfg) {
		return false
	}
	if isDir && !getBool(cfg, "only_dir", false) {
		// only_dir 未开启时，目录名也参与过滤
	} else if !isDir && getBool(cfg, "only_dir", false) {
		return false // 仅对目录生效时，文件不参与
	}
	keywords := getString(cfg, "keywords", "")
	if keywords == "" {
		return false
	}
	match := false
	if getBool(cfg, "regex_mode", false) {
		if re, err := regexp.Compile(keywords); err == nil {
			match = re.MatchString(name)
		}
	} else {
		for _, k := range strings.Split(keywords, ",") {
			if k = strings.TrimSpace(k); k != "" && strings.Contains(name, k) {
				match = true
				break
			}
		}
	}
	// filter_mode=true 时为筛选模式（仅保留匹配的），此时未匹配的跳过
	if getBool(cfg, "filter_mode", false) {
		return !match
	}
	return match
}

// ============ 任务请求延时 ============

// TaskDelay 任务运行时请求后延时，降低风控
type TaskDelay struct{}

func (p *TaskDelay) ID() string      { return "task_delay" }
func (p *TaskDelay) Name() string    { return "任务请求延时" }
func (p *TaskDelay) Version() string { return "0.1" }
func (p *TaskDelay) Enabled(cfg config.PluginConfig) bool {
	return cfg != nil && getBool(cfg, "enabled", false)
}

func (p *TaskDelay) ListDelay(env *Env) time.Duration {
	cfg := env.PluginConfig(p)
	if cfg == nil || !p.Enabled(cfg) {
		return 0
	}
	return time.Duration(getInt(cfg, "list_delay", 200)) * time.Millisecond
}

func (p *TaskDelay) CopyDelay(env *Env) time.Duration {
	cfg := env.PluginConfig(p)
	if cfg == nil || !p.Enabled(cfg) {
		return 0
	}
	return time.Duration(getInt(cfg, "copy_delay", 200)) * time.Millisecond
}
