package lb

import "testing"

func TestRoundRobinPick(t *testing.T) {
	// 轮询器需要稳定按照顺序依次返回实例。
	picker := NewRoundRobin()
	instances := []string{"10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080"}

	if got := picker.Pick(instances); got != "10.0.0.1:8080" {
		t.Fatalf("expected first instance, got %s", got)
	}
	if got := picker.Pick(instances); got != "10.0.0.2:8080" {
		t.Fatalf("expected second instance, got %s", got)
	}
	if got := picker.Pick(instances); got != "10.0.0.3:8080" {
		t.Fatalf("expected third instance, got %s", got)
	}
	if got := picker.Pick(instances); got != "10.0.0.1:8080" {
		t.Fatalf("expected round-robin reset, got %s", got)
	}
}

func TestRoundRobinPickEmpty(t *testing.T) {
	// 空实例列表由上层处理错误，这里只要求返回空字符串。
	picker := NewRoundRobin()
	if got := picker.Pick(nil); got != "" {
		t.Fatalf("expected empty result, got %s", got)
	}
}
