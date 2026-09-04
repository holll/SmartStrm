// Package version 提供构建版本信息与 GitHub 更新检查
package version

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 版本信息由 build.bat 通过 -ldflags 注入 main 包，main 包在 init 中转写到这里
var (
	Version   = "0.0.0"
	Commit    = ""
	BuildTime = ""
)

// RepoURL 项目主页（GitHub）
const RepoURL = "https://github.com/holll/SmartStrm"

const (
	apiLatestURL  = "https://api.github.com/repos/holll/SmartStrm/releases/latest"
	checkInterval = 10 * time.Minute // 两次真实请求的最短间隔（缓存时长）
	httpTimeout   = 6 * time.Second  // GitHub 不可达时的最大等待
)

// Release GitHub latest release 的部分字段
type Release struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
}

// UpdateInfo 关于页展示的全部信息
type UpdateInfo struct {
	Version     string    `json:"version"`                // 当前版本（去 v 前缀）
	Commit      string    `json:"commit,omitempty"`       // 构建提交
	BuildTime   string    `json:"build_time,omitempty"`   // 构建时间
	RepoURL     string    `json:"repo_url"`               // 项目地址
	IsLatest    bool      `json:"is_latest"`              // 是否为最新版本
	Latest      string    `json:"latest,omitempty"`       // 最新版本（tag 原文）
	LatestURL   string    `json:"latest_url,omitempty"`   // 最新版本下载页
	PublishedAt string    `json:"published_at,omitempty"` // 最新版本发布时间
	CheckedAt   time.Time `json:"checked_at"`             // 本次检查时间
	Error       string    `json:"error,omitempty"`        // 检查失败原因
}

var (
	mu      sync.Mutex
	cache   *UpdateInfo
	cacheAt time.Time
)

// Current 当前版本信息（不含更新状态）
func Current() UpdateInfo {
	return UpdateInfo{
		Version:   strings.TrimPrefix(Version, "v"),
		Commit:    Commit,
		BuildTime: BuildTime,
		RepoURL:   RepoURL,
	}
}

// Check 检查更新。缓存 10 分钟内直接返回上次结果；force 为 true 时忽略缓存。
// 请求失败时返回当前版本信息并附带 error，不影响页面展示
func Check(ctx context.Context, force bool) UpdateInfo {
	mu.Lock()
	if cache != nil && !force && time.Since(cacheAt) < checkInterval {
		info := *cache
		mu.Unlock()
		return info
	}
	mu.Unlock()

	info := Current()
	latest, rel, err := fetchLatest(ctx)
	if err != nil {
		info.Error = err.Error()
	} else {
		info.Latest = latest
		info.LatestURL = rel.HTMLURL
		info.PublishedAt = rel.PublishedAt
		info.IsLatest = compareVersions(info.Version, latest) >= 0
	}
	info.CheckedAt = time.Now()

	mu.Lock()
	cache = &info
	cacheAt = time.Now()
	mu.Unlock()
	return info
}

// fetchLatest 请求 GitHub API 获取最新 release 版本号（tag 原文）
func fetchLatest(ctx context.Context) (string, *Release, error) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiLatestURL, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "smartstrm-go/"+Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", nil, err
	}
	if rel.TagName == "" {
		return "", nil, fmt.Errorf("GitHub API 响应缺少 tag_name")
	}
	return strings.TrimPrefix(rel.TagName, "v"), &rel, nil
}

// semver 简易语义化版本（支持 v 前缀、缺省 minor/patch、pre-release 后缀）
type semver struct {
	major, minor, patch int
	pre                 string
}

var semverRe = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:-([0-9A-Za-z.\-]+))?$`)

func parseSemver(s string) (semver, bool) {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return semver{}, false
	}
	sem := semver{pre: m[4]}
	sem.major, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		sem.minor, _ = strconv.Atoi(m[2])
	}
	if m[3] != "" {
		sem.patch, _ = strconv.Atoi(m[3])
	}
	return sem, true
}

// compareVersions 语义化版本比较：a > b 返回 1，a == b 返回 0，a < b 返回 -1。
// 无法解析的版本按去掉 v 前缀后的字符串比较兜底
func compareVersions(a, b string) int {
	sa, oka := parseSemver(a)
	sb, okb := parseSemver(b)
	if !oka || !okb {
		return strings.Compare(strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v"))
	}
	cmp := func(x, y int) int {
		switch {
		case x > y:
			return 1
		case x < y:
			return -1
		}
		return 0
	}
	if d := cmp(sa.major, sb.major); d != 0 {
		return d
	}
	if d := cmp(sa.minor, sb.minor); d != 0 {
		return d
	}
	if d := cmp(sa.patch, sb.patch); d != 0 {
		return d
	}
	// pre-release 版本号更旧（v1.0.0-beta < v1.0.0）
	switch {
	case sa.pre == "" && sb.pre != "":
		return 1
	case sa.pre != "" && sb.pre == "":
		return -1
	case sa.pre != "" && sb.pre != "":
		return strings.Compare(sa.pre, sb.pre)
	}
	return 0
}
