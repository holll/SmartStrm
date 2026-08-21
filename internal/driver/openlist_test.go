package driver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// openlistTestServer 返回一个记录请求 body、按 path 分发且固定返回 code=200 的假 OpenList 服务
func openlistTestServer(t *testing.T, bodies map[string][]byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies[r.URL.Path] = b
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":200,"message":"success","data":null}`))
	}))
}

// TestOpenListRemoveBody 验证 /api/fs/remove 请求体为 {"dir", "names"} 对象，
// 防止回归为路径数组导致 "Empty file names"
func TestOpenListRemoveBody(t *testing.T) {
	bodies := map[string][]byte{}
	srv := openlistTestServer(t, bodies)
	defer srv.Close()

	o := NewOpenList(srv.URL, "token")
	if err := o.Remove(context.Background(), "/115/AV/M/MIDA-590"); err != nil {
		t.Fatalf("Remove 失败: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(bodies["/api/fs/remove"], &body); err != nil {
		t.Fatalf("请求体解析失败: %s (%v)", bodies["/api/fs/remove"], err)
	}
	if body["dir"] != "/115/AV/M" {
		t.Fatalf("dir 错误: %v", body["dir"])
	}
	names, ok := body["names"].([]any)
	if !ok || len(names) != 1 || names[0] != "MIDA-590" {
		t.Fatalf("names 错误: %v", body["names"])
	}
}

// TestOpenListMkdirMove 验证 mkdir / move 请求体格式
func TestOpenListMkdirMove(t *testing.T) {
	bodies := map[string][]byte{}
	srv := openlistTestServer(t, bodies)
	defer srv.Close()

	o := NewOpenList(srv.URL, "token")
	ctx := context.Background()

	if err := o.Mkdir(ctx, "/115/AV/M/MIDA-590"); err != nil {
		t.Fatalf("Mkdir 失败: %v", err)
	}
	var mk map[string]any
	_ = json.Unmarshal(bodies["/api/fs/mkdir"], &mk)
	if mk["path"] != "/115/AV/M/MIDA-590" {
		t.Fatalf("mkdir path 错误: %v", mk["path"])
	}

	if err := o.Move(ctx, "/115/AV/M", "/115/AV/T", []string{"MDX-123"}); err != nil {
		t.Fatalf("Move 失败: %v", err)
	}
	var mv map[string]any
	_ = json.Unmarshal(bodies["/api/fs/move"], &mv)
	if mv["src_dir"] != "/115/AV/M" || mv["dst_dir"] != "/115/AV/T" {
		t.Fatalf("move 目录错误: %v", mv)
	}
	names, ok := mv["names"].([]any)
	if !ok || len(names) != 1 || names[0] != "MDX-123" {
		t.Fatalf("move names 错误: %v", mv["names"])
	}
}

// TestSplitDirPath 验证路径拆分
func TestSplitDirPath(t *testing.T) {
	cases := []struct{ in, dir, name string }{
		{"/115/AV/M/MIDA-590", "/115/AV/M", "MIDA-590"},
		{"/115/AV/M/MIDA-590/", "/115/AV/M", "MIDA-590"},
		{"/MIDA-590", "/", "MIDA-590"},
		{"/", "/", ""},
		{"MIDA-590", "/", "MIDA-590"},
	}
	for _, c := range cases {
		dir, name := SplitPath(c.in)
		if dir != c.dir || name != c.name {
			t.Errorf("SplitPath(%q) = (%q,%q), 期望 (%q,%q)", c.in, dir, name, c.dir, c.name)
		}
	}
}

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
