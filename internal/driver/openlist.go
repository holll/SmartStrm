package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrUnsupportedDriver 不支持的驱动类型
	ErrUnsupportedDriver = errors.New("不支持的驱动类型，目前仅支持 openlist")
	// ErrNotFound 路径不存在
	ErrNotFound = errors.New("路径不存在")
)

// OpenList OpenList/AList 驱动
type OpenList struct {
	base    string
	token   string
	client  *http.Client
	limiter *rateLimiter // 按 base 共享的 API 限速器（115 限流）
}

// NewOpenList 创建 OpenList 驱动
func NewOpenList(base, token string) *OpenList {
	return &OpenList{
		base:    stringsTrimSuffix(base),
		token:   token,
		client:  &http.Client{Timeout: 30 * time.Second},
		limiter: getLimiter(stringsTrimSuffix(base)),
	}
}

func stringsTrimSuffix(s string) string {
	for len(s) > 1 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// apiResp OpenList API 通用响应
type apiResp struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// listData /api/fs/list 响应
type listData struct {
	Content []struct {
		Name     string `json:"name"`
		Size     int64  `json:"size"`
		IsDir    bool   `json:"is_dir"`
		Modified string `json:"modified"`
	} `json:"content"`
	Total int64 `json:"total"`
}

// parseOpenListTime 兼容解析 OpenList 返回的时间格式：
// 新版：RFC3339 纳秒（"2026-01-29T16:27:26.947903156+08:00"）
// 旧版：无时区（"2006-01-02 15:04:05"）、纯日期、unix 秒等
// 解析失败返回零值
func parseOpenListTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	// 带时区格式
	for _, l := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	// 无时区格式按本地时区解析
	for _, l := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t
		}
	}
	// unix 秒/毫秒
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			n /= 1000 // 毫秒
		}
		return time.Unix(n, 0)
	}
	return time.Time{}
}

// post 向 OpenList API 发 POST 请求（经限速器排队，防触发远端限流）
func (o *OpenList) post(ctx context.Context, api string, payload any) (apiResp, error) {
	if err := o.limiter.wait(ctx); err != nil {
		return apiResp{}, err
	}
	var resp apiResp
	body, err := json.Marshal(payload)
	if err != nil {
		return resp, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+api, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.token != "" {
		req.Header.Set("Authorization", o.token)
	}
	r, err := o.client.Do(req)
	if err != nil {
		return resp, err
	}
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(data, &resp); err != nil {
		return resp, fmt.Errorf("OpenList 响应解析失败: %s", string(data))
	}
	if resp.Code != 200 {
		return resp, fmt.Errorf("OpenList %s: code=%d message=%s", api, resp.Code, resp.Message)
	}
	return resp, nil
}

// List 列出目录内容，处理分页
func (o *OpenList) List(ctx context.Context, path string) ([]File, error) {
	var files []File
	page := 1
	for {
		resp, err := o.post(ctx, "/api/fs/list", map[string]any{
			"path":     path,
			"password": "",
			"page":     page,
			"per_page": 200,
			"refresh":  false,
		})
		if err != nil {
			return nil, err
		}
		if string(resp.Data) == "null" || len(resp.Data) == 0 {
			return nil, ErrNotFound
		}
		var d listData
		if err := json.Unmarshal(resp.Data, &d); err != nil {
			return nil, err
		}
		for _, c := range d.Content {
			f := File{Name: c.Name, Size: c.Size, IsDir: c.IsDir}
			f.Modified = parseOpenListTime(c.Modified)
			files = append(files, f)
		}
		// 服务端返回 total 时用它判断是否还有下一页，避免恰好 200 条时的多余空请求
		if d.Total > 0 && int64(page)*200 >= d.Total {
			break
		}
		if len(d.Content) < 200 {
			break
		}
		page++
	}
	return files, nil
}

// Remove 删除路径（文件或目录）
// OpenList/AList 的 /api/fs/remove 要求请求体为路径数组，如 ["/a/b"]；
// 传 {"path": ...} 对象会被服务端解析为空列表，报 "Empty file names"
func (o *OpenList) Remove(ctx context.Context, path string) error {
	_, err := o.post(ctx, "/api/fs/remove", []string{path})
	return err
}

// Rename 重命名
func (o *OpenList) Rename(ctx context.Context, path, newName string) error {
	_, err := o.post(ctx, "/api/fs/rename", map[string]any{"path": path, "name": newName})
	return err
}

// DownloadURL 构造路径兼容模式的直链 URL（OpenList /d/ 下载路径）
// 若配置了 StrmBase 则使用它，否则使用存储自身地址
func (o *OpenList) DownloadURL(path string) string {
	return o.base + "/d" + path
}

// Base 返回存储基地址
func (o *OpenList) Base() string { return o.base }
