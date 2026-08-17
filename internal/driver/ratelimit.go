package driver

import (
	"context"
	"sync"
	"time"
)

// apiRateInterval OpenList API 限流间隔（115drive 限流 1r/s，按存储 base 共享）
const apiRateInterval = time.Second

// rateLimiter 令牌桶限速器：每次 wait 预留一个请求槽位，
// 并发调用按到达顺序排队，保证任意时刻请求间隔 ≥ interval
type rateLimiter struct {
	mu       sync.Mutex
	next     time.Time // 下一个槽位可用时间
	interval time.Duration
}

// limiters 按存储 base 共享的限速器（115 限流按账号计数，跨任务/实例共享）
var (
	limitersMu sync.Mutex
	limiters   = map[string]*rateLimiter{}
)

func getLimiter(base string) *rateLimiter {
	limitersMu.Lock()
	defer limitersMu.Unlock()
	l, ok := limiters[base]
	if !ok {
		l = &rateLimiter{interval: apiRateInterval}
		limiters[base] = l
	}
	return l
}

// wait 预留一个请求槽位并等待其可用；ctx 取消时立即返回错误。
// 槽位时间由调用时点与前序预留决定，等待期间不再参与排队
func (l *rateLimiter) wait(ctx context.Context) error {
	l.mu.Lock()
	slot := time.Now()
	if slot.Before(l.next) {
		slot = l.next // 前序请求尚未放行，取下一个可用槽位
	}
	l.next = slot.Add(l.interval)
	l.mu.Unlock()

	delay := time.Until(slot)
	if delay <= 0 {
		return nil
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
