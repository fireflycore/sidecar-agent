package breaker

import (
	"errors"
	"sync"
	"time"
)

// State 表示熔断器当前状态。
type State int

const (
	// StateClosed 表示当前允许正常放行请求。
	StateClosed State = iota
	// StateOpen 表示当前已经断路，不再放行请求。
	StateOpen
	// StateHalfOpen 表示冷却期已过，允许少量探测请求。
	StateHalfOpen
)

var ErrCircuitOpen = errors.New("circuit breaker open")

// Breaker 是 V3 当前阶段的最小滑动窗口熔断器。
//
// 它基于累计窗口统计失败率：
// - 达到最小样本数后开始计算失败率
// - 失败率达到阈值时进入 open
// - 冷却时间结束后进入 half-open
// - half-open 成功则恢复，失败则重新断路
type Breaker struct {
	mu sync.Mutex

	state State

	failures int
	total    int

	threshold   float64
	minRequests int
	cooldown    time.Duration
	openAt      time.Time
}

// New 创建一个最小熔断器。
func New(threshold float64, minRequests int, cooldown time.Duration) *Breaker {
	return &Breaker{
		state:       StateClosed,
		threshold:   threshold,
		minRequests: minRequests,
		cooldown:    cooldown,
	}
}

// Allow 判断当前请求是否允许继续访问下游。
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateOpen:
		// 冷却期结束后进入 half-open，让少量请求探测恢复情况。
		if time.Since(b.openAt) >= b.cooldown {
			b.state = StateHalfOpen
			return nil
		}
		return ErrCircuitOpen
	default:
		return nil
	}
}

// Record 记录一次调用结果。
func (b *Breaker) Record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == StateHalfOpen {
		// 半开阶段只看当前这次探测请求结果。
		if success {
			b.reset()
		} else {
			b.trip()
		}
		return
	}

	b.total++
	if !success {
		b.failures++
	}

	if b.total < b.minRequests {
		return
	}

	failureRate := float64(b.failures) / float64(b.total)
	if failureRate >= b.threshold {
		b.trip()
	}
}

// State 返回当前熔断器状态，便于测试和观测。
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *Breaker) trip() {
	b.state = StateOpen
	b.openAt = time.Now()
}

func (b *Breaker) reset() {
	b.state = StateClosed
	b.failures = 0
	b.total = 0
	b.openAt = time.Time{}
}
