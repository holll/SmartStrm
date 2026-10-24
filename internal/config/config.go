// Package config 定义运行时配置结构（持久化于数据库 settings 表，无配置文件）
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/robfig/cron/v3"
)

// STRMConfig STRM 生成全局设置
type STRMConfig struct {
	MediaExt  []string `json:"media_ext"`  // 媒体文件后缀，这些文件生成 STRM
	MediaSize int      `json:"media_size"` // 媒体文件大小阈值 MB，大于此才生成
	CopyExt   []string `json:"copy_ext"`   // 复制到本地的文件后缀（刮削文件）
	SaveDir   string   `json:"save_dir"`   // STRM 生成根目录（默认相对路径 ./strm）
	URLEncode bool     `json:"url_encode"` // 对 STRM 内 URL 编码
	StrmBase  string   `json:"strm_base"`  // 写入 STRM 的直链前缀（留空使用存储自身地址）
}

// EmbyDeleteSync Emby 删除同步设置
type EmbyDeleteSync struct {
	Enabled        bool     `json:"enabled"`
	StrmInEmby     string   `json:"strm_in_emby"`     // strm 目录在 Emby 容器内的路径
	StoragePathMap string   `json:"storage_path_map"` // strm 目录对应的存储内路径前缀
	Storage        string   `json:"storage"`          // 删除操作使用的存储名
	AllowedPrefix  []string `json:"allowed_prefix"`   // 只允许删除这些路径前缀，防误删
}

// WebhookConfig Webhook 设置
type WebhookConfig struct {
	Token          string         `json:"token"` // 触发 URL 的鉴权 token
	EmbyDeleteSync EmbyDeleteSync `json:"emby_delete_sync"`
}

// PluginConfig 单个插件配置（任意 JSON 对象）
type PluginConfig map[string]any

// Storage 存储定义
type Storage struct {
	Name   string `json:"name"`
	Driver string `json:"driver"` // 目前仅支持 openlist
	URL    string `json:"url"`
	Token  string `json:"token"`
}

// Task 任务定义
type Task struct {
	Name         string                  `json:"name"`
	Storage      string                  `json:"storage"`
	StoragePath  string                  `json:"storage_path"` // 存储中媒体文件路径
	Crontab      string                  `json:"crontab"`      // 空则不定时
	Incremental  bool                    `json:"incremental"`  // 增量生成；false 为同步
	DirTimeCheck bool                    `json:"dir_time_check"`
	KeepLocalAst bool                    `json:"keep_local_asset"` // 同步模式保留本地刮削文件
	Plugins      map[string]PluginConfig `json:"plugins"`          // 任务级插件配置，覆盖全局
}

// Config 运行时配置（全部来自数据库与命令行参数，无配置文件）
type Config struct {
	mu     sync.RWMutex
	Server struct {
		Port     int    `json:"-"` // 命令行 -port
		Username string `json:"-"` // 固定 admin
	} `json:"-"`

	STRM     STRMConfig              `json:"strm"`
	Webhook  WebhookConfig           `json:"webhook"`  // 数据库 webhook 表
	Plugins  map[string]PluginConfig `json:"plugins"`  // 数据库 settings["plugins"]
	Storages []Storage               `json:"storages"` // 数据库 storages 表
	Tasks    []Task                  `json:"tasks"`    // 数据库 tasks 表
}

// DefaultSTRM 返回 STRM 设置默认值
// 注意：save_dir 默认使用相对路径 ./strm（Go 程序无 Docker 挂载限制；
// Docker 部署可在 Web 设置中改为容器内/挂载路径）
func DefaultSTRM() STRMConfig {
	return STRMConfig{
		MediaExt:  []string{"mp4", "mkv", "mov", "avi", "wmv"},
		MediaSize: 20,
		CopyExt:   []string{"nfo", "jpg", "png", "ass", "srt"},
		SaveDir:   "./strm",
		URLEncode: true,
		StrmBase:  "",
	}
}

// DefaultPlugins 返回插件全局配置默认值
func DefaultPlugins() map[string]PluginConfig {
	return map[string]PluginConfig{}
}

// Snapshot returns an isolated deep copy. Runtime readers must never retain live fields.
func (c *Config) Snapshot() *Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clone()
}
func (c *Config) clone() *Config {
	out := &Config{STRM: c.STRM, Webhook: c.Webhook, Plugins: ClonePlugins(c.Plugins), Storages: append([]Storage(nil), c.Storages...), Tasks: append([]Task(nil), c.Tasks...)}
	out.Server = c.Server
	out.STRM.MediaExt = append([]string(nil), c.STRM.MediaExt...)
	out.STRM.CopyExt = append([]string(nil), c.STRM.CopyExt...)
	out.Webhook.EmbyDeleteSync.AllowedPrefix = append([]string(nil), c.Webhook.EmbyDeleteSync.AllowedPrefix...)
	for i := range out.Tasks {
		out.Tasks[i].Plugins = ClonePlugins(out.Tasks[i].Plugins)
	}
	return out
}

// Replace publishes a complete configuration. Callers serialize read/modify/write operations.
func (c *Config) Replace(next *Config) {
	n := next.Snapshot()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Server, c.STRM, c.Webhook, c.Plugins, c.Storages, c.Tasks = n.Server, n.STRM, n.Webhook, n.Plugins, n.Storages, n.Tasks
}
func cloneValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := map[string]any{}
		for k, v := range x {
			m[k] = cloneValue(v)
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = cloneValue(v)
		}
		return a
	case []string:
		return append([]string(nil), x...)
	default:
		return v
	}
}
func ClonePlugins(in map[string]PluginConfig) map[string]PluginConfig {
	if in == nil {
		return nil
	}
	out := map[string]PluginConfig{}
	for id, cfg := range in {
		m := PluginConfig{}
		for k, v := range cfg {
			m[k] = cloneValue(v)
		}
		out[id] = m
	}
	return out
}

// TaskDir accepts nested names, but rejects traversal, aliases and symlink components.
func TaskDir(root, name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("非法任务名")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("非法任务路径组件")
		}
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(base, filepath.FromSlash(name))
	// Reject existing symlinks, including ancestors of the configured root.
	for p := dst; ; p = filepath.Dir(p) {
		if st, e := os.Lstat(p); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("任务路径包含符号链接: %s", p)
		} else if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return dst, nil
}
func ValidateTask(root string, t Task, tasks []Task, replacing string) error {
	if err := ValidatePlugins(t.Plugins); err != nil {
		return err
	}
	if _, err := TaskDir(root, t.Name); err != nil {
		return err
	}
	if t.Storage == "" || t.StoragePath == "" {
		return fmt.Errorf("存储、路径不能为空")
	}
	if t.Crontab != "" {
		if _, err := cron.ParseStandard(t.Crontab); err != nil {
			return fmt.Errorf("定时表达式无效: %w", err)
		}
	}
	for _, other := range tasks {
		if other.Name == replacing {
			continue
		}
		if t.Name == other.Name || strings.HasPrefix(t.Name, other.Name+"/") || strings.HasPrefix(other.Name, t.Name+"/") {
			return fmt.Errorf("任务目录重叠: %s", other.Name)
		}
	}
	return nil
}

// ValidatePlugins rejects malformed enabled regular expressions before any writes or cleanup.
func ValidatePlugins(all map[string]PluginConfig) error {
	for id, cfg := range all {
		enabled, _ := cfg["enabled"].(bool)
		regex, _ := cfg["regex_mode"].(bool)
		if !enabled || !regex {
			continue
		}
		key := ""
		switch id {
		case "skip_keyword":
			key = "keywords"
		case "content_replace":
			key = "find_text"
		}
		if key == "" {
			continue
		}
		pattern, _ := cfg[key].(string)
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("插件 %s 正则无效: %w", id, err)
		}
	}
	return nil
}
