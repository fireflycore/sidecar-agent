package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/fireflycore/sidecar-agent/model"
)

// fakeSource 提供 discovery 测试需要的最小数据源桩。
type fakeSource struct {
	// snapshots 按调用顺序返回不同快照。
	snapshots [][]model.ServiceInstance
	// index 记录当前读取位置。
	index int
}

// Discover 返回当前轮次的实例快照。
func (f *fakeSource) Discover(_ context.Context) ([]model.ServiceInstance, error) {
	// 若已超出测试数据范围，则固定返回最后一版快照。
	if f.index >= len(f.snapshots) {
		return f.snapshots[len(f.snapshots)-1], nil
	}
	// 取出当前轮次结果。
	snapshot := f.snapshots[f.index]
	// 读取后推进索引。
	f.index++
	// 返回当前快照。
	return snapshot, nil
}

// fakeMetrics 记录刷新次数。
type fakeMetrics struct {
	// refreshCount 保存累计刷新次数。
	refreshCount int
}

// IncDiscoveryRefresh 仅累加刷新计数。
func (f *fakeMetrics) IncDiscoveryRefresh() {
	// 每次刷新都加一。
	f.refreshCount++
}

// TestRefreshNowPublishesOnlyWhenSnapshotChanges 验证相同快照不会重复发布。
func TestRefreshNowPublishesOnlyWhenSnapshotChanges(t *testing.T) {
	// 先准备两次相同快照与一次变化快照。
	source := &fakeSource{
		snapshots: [][]model.ServiceInstance{
			{
				{
					Name:      "auth",
					Namespace: "default",
					DNS:       "auth.default.svc.cluster.local",
					Env:       "prod",
					Address:   "10.0.0.10",
					Port:      9090,
					Weight:    100,
				},
			},
			{
				{
					Name:      "auth",
					Namespace: "default",
					DNS:       "auth.default.svc.cluster.local",
					Env:       "prod",
					Address:   "10.0.0.10",
					Port:      9090,
					Weight:    100,
				},
			},
			{
				{
					Name:      "auth",
					Namespace: "default",
					DNS:       "auth.default.svc.cluster.local",
					Env:       "prod",
					Address:   "10.0.0.11",
					Port:      9090,
					Weight:    100,
				},
			},
		},
	}
	metrics := &fakeMetrics{}
	watcher := New(source, time.Second, 0, nil, metrics)
	// 记录 onUpdate 实际触发次数。
	updateCount := 0
	onUpdate := func(instances []model.ServiceInstance) error {
		// 每发布一次快照就加一。
		updateCount++
		return nil
	}
	// 第一次快照应触发发布。
	if err := watcher.RefreshNow(context.Background(), onUpdate); err != nil {
		t.Fatalf("first refresh failed: %v", err)
	}
	// 第二次相同快照不应再次发布。
	if err := watcher.RefreshNow(context.Background(), onUpdate); err != nil {
		t.Fatalf("second refresh failed: %v", err)
	}
	// 第三次快照变化后应再次发布。
	if err := watcher.RefreshNow(context.Background(), onUpdate); err != nil {
		t.Fatalf("third refresh failed: %v", err)
	}
	// 最终应只发布两次。
	if got, want := updateCount, 2; got != want {
		t.Fatalf("unexpected update count: got=%d want=%d", got, want)
	}
	// 发现刷新次数应累计三次。
	if got, want := metrics.refreshCount, 3; got != want {
		t.Fatalf("unexpected refresh count: got=%d want=%d", got, want)
	}
}
