// Package config 定义运行时配置结构（持久化于数据库 settings 表，无配置文件）
package config

// STRMConfig STRM 生成全局设置
type STRMConfig struct {
	MediaExt   []string `json:"media_ext"`   // 媒体文件后缀，这些文件生成 STRM
	MediaSize  int      `json:"media_size"`  // 媒体文件大小阈值 MB，大于此才生成
	CopyExt    []string `json:"copy_ext"`    // 复制到本地的文件后缀（刮削文件）
	SaveDir    string   `json:"save_dir"`    // STRM 生成根目录（默认相对路径 ./strm）
	URLEncode  bool     `json:"url_encode"`  // 对 STRM 内 URL 编码
	GenType    string   `json:"gen_type"`    // 生成类型: path=路径兼容模式 fid=文件编号模式
	StrmBase   string   `json:"strm_base"`   // 写入 STRM 的直链前缀（留空使用存储自身地址）
}

// EmbyDeleteSync Emby 删除同步设置
type EmbyDeleteSync struct {
	Enabled        bool     `json:"enabled"`
	StrmInEmby     string   `json:"strm_in_emby"`      // strm 目录在 Emby 容器内的路径
	StoragePathMap string   `json:"storage_path_map"`  // strm 目录对应的存储内路径前缀
	Storage        string   `json:"storage"`           // 删除操作使用的存储名
	AllowedPrefix  []string `json:"allowed_prefix"`    // 只允许删除这些路径前缀，防误删
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
	Name          string                  `json:"name"`
	Storage       string                  `json:"storage"`
	StoragePath   string                  `json:"storage_path"` // 存储中媒体文件路径
	Crontab       string                  `json:"crontab"`      // 空则不定时
	Incremental   bool                    `json:"incremental"`  // 增量生成；false 为同步
	DirTimeCheck  bool                    `json:"dir_time_check"`
	KeepLocalAst  bool                    `json:"keep_local_asset"` // 同步模式保留本地刮削文件
	Plugins       map[string]PluginConfig `json:"plugins"`         // 任务级插件配置，覆盖全局
}

// Config 运行时配置（全部来自数据库与命令行参数，无配置文件）
type Config struct {
	Server struct {
		Port     int    `json:"-"` // 命令行 -port
		Username string `json:"-"` // 固定 admin
	} `json:"-"`

	STRM     STRMConfig               `json:"strm"`
	Webhook  WebhookConfig            `json:"webhook"` // 数据库 webhook 表
	Plugins  map[string]PluginConfig  `json:"plugins"` // 数据库 settings["plugins"]
	Storages []Storage                `json:"storages"` // 数据库 storages 表
	Tasks    []Task                   `json:"tasks"`    // 数据库 tasks 表
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
		GenType:   "path",
		StrmBase:  "",
	}
}

// DefaultPlugins 返回插件全局配置默认值
func DefaultPlugins() map[string]PluginConfig {
	return map[string]PluginConfig{}
}
