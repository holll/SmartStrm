package driver

import (
	"context"
	"testing"
	"time"
)

// TestRateLimiterInterval 单 goroutine 连续 wait：相邻请求间隔 ≥ interval
func TestRateLimiterInterval(t *testing.T) {
	l := &rateLimiter{interval: 30 * time.Millisecond}
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := l.wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	// 3 个间隔，允许少量调度抖动
	if elapsed < 90*time.Millisecond {
		t.Errorf("4 次请求耗时 %v，未达到 30ms 间隔的预期", elapsed)
	}
}

// TestRateLimiterConcurrent 并发 wait：各请求按预留槽位串行通过
func TestRateLimiterConcurrent(t *testing.T) {
	l := &rateLimiter{interval: 20 * time.Millisecond}
	ctx := context.Background()
	done := make(chan time.Time, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_ = l.wait(ctx)
			done <- time.Now()
		}()
	}
	first := <-done
	for i := 0; i < 7; i++ {
		next := <-done
		if next.Sub(first) < 0 {
			t.Errorf("请求完成顺序异常: %v 早于 %v", next, first)
		}
		first = next
	}
}

// TestRateLimiterContextCancel 等待期间 ctx 取消应立即返回
func TestRateLimiterContextCancel(t *testing.T) {
	l := &rateLimiter{interval: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	if err := l.wait(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if err := l.wait(ctx); err == nil {
		t.Fatal("ctx 取消后 wait 应返回错误")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("ctx 取消后 wait 未及时返回，耗时 %v", time.Since(start))
	}
}

// TestSharedLimiter 同一 base 共享同一限速器实例
func TestSharedLimiter(t *testing.T) {
	a := NewOpenList("https://alist.example.com/", "tok")
	b := NewOpenList("https://alist.example.com", "tok")
	if a.limiter != b.limiter {
		t.Error("同一 base 应共享限速器")
	}
	c := NewOpenList("https://other.example.com", "tok")
	if a.limiter == c.limiter {
		t.Error("不同 base 不应共享限速器")
	}
}
