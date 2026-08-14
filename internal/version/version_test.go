package version

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.1.2", "v0.1.2", 0},
		{"0.1.2", "v0.1.2", 0},
		{"v0.1.3", "v0.1.2", 1},
		{"v0.1.2", "v0.1.3", -1},
		{"v1.0.0", "v0.9.9", 1},
		{"v0.10.0", "v0.9.0", 1}, // 两位数 minor
		{"v0.1.2", "v0.1", 1},     // 缺省 patch 视为 0
		{"v0.1.0", "v0.1", 0},
		{"v1.0.0-beta", "v1.0.0", -1}, // pre-release 更旧
		{"v1.0.0", "v1.0.0-beta", 1},
		{"v1.0.0-beta", "v1.0.0-alpha", 1},
		{"0.0.0", "v0.1.2", -1}, // 本地开发版应判为有更新
		{"dev", "release", -1},  // 无法解析时字符串比较兜底（"dev" < "release"）
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCurrentTrimsV(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v0.1.2"
	if got := Current().Version; got != "0.1.2" {
		t.Errorf("Current().Version = %q, want %q", got, "0.1.2")
	}
}
