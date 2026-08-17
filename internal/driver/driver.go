// Package driver 定义存储驱动接口并实现 OpenList/AList 驱动
package driver

import (
	"context"
	"time"
)

// File 远端文件/目录信息
type File struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	IsDir    bool      `json:"is_dir"`
	Modified time.Time `json:"modified"`
}

// Driver 存储驱动接口
type Driver interface {
	// List 列出目录内容
	List(ctx context.Context, path string) ([]File, error)
	// Remove 删除文件或目录（Emby 删除同步、同步模式清理）
	Remove(ctx context.Context, path string) error
	// Rename 重命名（预留）
	Rename(ctx context.Context, path, newName string) error
}

// New 创建驱动
func New(driverName, url, token string) (Driver, error) {
	switch driverName {
	case "openlist", "alist":
		return NewOpenList(url, token), nil
	default:
		return nil, ErrUnsupportedDriver
	}
}
