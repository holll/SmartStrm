package driver

import (
	"testing"
	"time"
)

func TestParseOpenListTime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time // 仅验证非零值与字段
	}{
		{"2026-01-29T16:27:26.947903156+08:00", time.Date(2026, 1, 29, 16, 27, 26, 0, time.FixedZone("", 8*3600))},
		{"2026-01-29T16:27:26+08:00", time.Date(2026, 1, 29, 16, 27, 26, 0, time.FixedZone("", 8*3600))},
		{"2026-01-29T16:27:26Z", time.Date(2026, 1, 29, 16, 27, 26, 0, time.UTC)},
		{"2026-01-29 16:27:26", time.Date(2026, 1, 29, 16, 27, 26, 0, time.Local)},
		{"2026-01-29", time.Date(2026, 1, 29, 0, 0, 0, 0, time.Local)},
		{"1760000000", time.Unix(1760000000, 0)},
		{"1760000000000", time.Unix(1760000000, 0)},
		{"", time.Time{}},
		{"not-a-date", time.Time{}},
	}
	for _, c := range cases {
		got := parseOpenListTime(c.in)
		if c.want.IsZero() {
			if !got.IsZero() {
				t.Errorf("parseOpenListTime(%q) 应为零值，得到 %v", c.in, got)
			}
			continue
		}
		if got.IsZero() {
			t.Errorf("parseOpenListTime(%q) 解析失败", c.in)
			continue
		}
		// 字段级比较（忽略纳秒）
		if got.Year() != c.want.Year() || got.Month() != c.want.Month() || got.Day() != c.want.Day() ||
			got.Hour() != c.want.Hour() || got.Minute() != c.want.Minute() || got.Second() != c.want.Second() {
			t.Errorf("parseOpenListTime(%q) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}
