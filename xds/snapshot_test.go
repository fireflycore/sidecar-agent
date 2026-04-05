package xds

import (
	"testing"
	"time"

	"github.com/fireflycore/sidecar-agent/model"
)

// TestBuilderBuild 验证快照构建器能够产出一版一致快照。
func TestBuilderBuild(t *testing.T) {
	// 创建与文档基线一致的构建参数。
	builder := NewBuilder(BuilderSettings{
		NodeID:             "node-dev",
		ListenerAddress:    "127.0.0.1:15001",
		RouteName:          "local-route",
		LocalCluster:       "cluster-a",
		DefaultTimeout:     3 * time.Second,
		RetryAttempts:      2,
		PerTryTimeout:      1 * time.Second,
		MaxConnections:     1024,
		MaxPendingRequests: 2048,
		MaxRequests:        4096,
		Consecutive5XX:     5,
		BaseEjectionTime:   30 * time.Second,
		MaxEjectionPercent: 50,
	})
	// 构造同一服务的本地与远端两个实例。
	snapshot, summary, err := builder.Build([]model.ServiceInstance{
		{
			Name:      "auth",
			Namespace: "default",
			DNS:       "auth.default.svc.cluster.local",
			Env:       "prod",
			Zone:      "idc-a-1",
			Address:   "10.0.0.10",
			Port:      9090,
			Weight:    100,
			Cluster:   "cluster-a",
		},
		{
			Name:      "auth",
			Namespace: "default",
			DNS:       "auth.default.svc.cluster.local",
			Env:       "prod",
			Zone:      "idc-b-1",
			Address:   "10.0.0.20",
			Port:      9090,
			Weight:    100,
			Cluster:   "cluster-b",
		},
	})
	// 构建必须成功。
	if err != nil {
		t.Fatalf("build snapshot failed: %v", err)
	}
	// 快照不能为空。
	if snapshot == nil {
		t.Fatal("expected snapshot to be created")
	}
	// 同一逻辑服务应只生成一个 cluster。
	if got, want := summary.ClusterCount, 1; got != want {
		t.Fatalf("unexpected cluster count: got=%d want=%d", got, want)
	}
	// 两个实例都应进入 endpoint 集合。
	if got, want := summary.EndpointCount, 2; got != want {
		t.Fatalf("unexpected endpoint count: got=%d want=%d", got, want)
	}
	// 服务列表应包含 auth.default。
	if got, want := len(summary.Services), 1; got != want {
		t.Fatalf("unexpected service count: got=%d want=%d", got, want)
	}
	if got, want := summary.Services[0], "auth.default"; got != want {
		t.Fatalf("unexpected service name: got=%s want=%s", got, want)
	}
}
