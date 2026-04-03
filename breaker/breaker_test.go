package breaker

import (
	"errors"
	"testing"
	"time"
)

func TestBreakerTripWhenFailureRateReached(t *testing.T) {
	// 当请求数达到最小阈值且失败率超过阈值时，应立即进入 open。
	b := New(0.5, 4, 20*time.Millisecond)

	b.Record(false)
	b.Record(false)
	b.Record(true)
	b.Record(true)

	if got := b.State(); got != StateOpen {
		t.Fatalf("expected StateOpen, got %v", got)
	}
	if err := b.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}
}

func TestBreakerHalfOpenAndReset(t *testing.T) {
	// 冷却期结束后进入 half-open，探测成功应恢复到 closed。
	b := New(0.5, 2, 10*time.Millisecond)

	b.Record(false)
	b.Record(true)

	time.Sleep(20 * time.Millisecond)
	if err := b.Allow(); err != nil {
		t.Fatalf("expected half-open allow, got %v", err)
	}
	if got := b.State(); got != StateHalfOpen {
		t.Fatalf("expected StateHalfOpen, got %v", got)
	}

	b.Record(true)
	if got := b.State(); got != StateClosed {
		t.Fatalf("expected StateClosed after recovery, got %v", got)
	}
}

func TestBreakerHalfOpenFailureTripsAgain(t *testing.T) {
	// 半开探测失败时应重新进入 open。
	b := New(0.5, 2, 10*time.Millisecond)

	b.Record(false)
	b.Record(true)

	time.Sleep(20 * time.Millisecond)
	if err := b.Allow(); err != nil {
		t.Fatalf("expected half-open allow, got %v", err)
	}

	b.Record(false)
	if got := b.State(); got != StateOpen {
		t.Fatalf("expected StateOpen after failed probe, got %v", got)
	}
}
