package task

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// TestCronChinaLocation cron 表达式应按中国时区（UTC+8）解释：
// `44 4 * * *` 的下次触发应为北京时间 04:44（即 UTC 前一天 20:44），
// 而非服务器 UTC 时区下的 04:44（= 北京时间 12:44）
func TestCronChinaLocation(t *testing.T) {
	c := cron.New(cron.WithLocation(chinaTZ))
	id, err := c.AddFunc("44 4 * * *", func() {})
	if err != nil {
		t.Fatalf("AddFunc: %v", err)
	}
	c.Start()
	defer c.Stop()
	// run() 启动即计算各 entry.Next，轮询等待其就绪
	var next time.Time
	deadline := time.Now().Add(2 * time.Second)
	for next.IsZero() {
		next = c.Entry(id).Next
		if !next.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cron 未计算下次运行时间")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if next.Location() != chinaTZ {
		t.Fatalf("cron 应使用中国时区解释，got %v", next.Location())
	}
	if next.Hour() != 4 || next.Minute() != 44 {
		t.Fatalf("Next 应为北京时间 04:44，got %v", next)
	}
	if utc := next.UTC(); utc.Hour() != 20 || utc.Minute() != 44 {
		t.Fatalf("北京时间 04:44 应为 UTC 前一天 20:44，got %v", utc)
	}
}
