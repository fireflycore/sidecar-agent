package proxy

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type fakeResolver struct {
	instances []string
}

func (f *fakeResolver) Resolve(serviceName string) ([]string, error) {
	return f.instances, nil
}

type staticPicker struct {
	target string
}

func (p *staticPicker) Pick(instances []string) string {
	if p.target != "" {
		return p.target
	}
	if len(instances) == 0 {
		return ""
	}
	return instances[0]
}

func TestParseServiceName(t *testing.T) {
	// authority 解析必须只拿到逻辑服务名。
	if got := parseServiceName("auth.default.svc.cluster.local:9090"); got != "auth" {
		t.Fatalf("expected auth, got %s", got)
	}
}

func TestProxyServerForwardsGRPCByAuthority(t *testing.T) {
	// 这里起一个真实 gRPC 后端，再通过代理转发，验证 authority 解析和透明转发链路。
	backendAddr, stopBackend := startHealthBackend(t)
	defer stopBackend()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}

	server := New(&fakeResolver{
		instances: []string{backendAddr},
	}, &staticPicker{})

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	defer func() {
		_ = server.Shutdown()
		select {
		case serveErr := <-errCh:
			if serveErr != nil {
				t.Fatalf("proxy serve failed: %v", serveErr)
			}
		case <-time.After(time.Second):
			t.Fatal("proxy server did not stop in time")
		}
	}()

	conn, err := grpc.DialContext(
		context.Background(),
		listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithAuthority("auth.default.svc.cluster.local:9090"),
	)
	if err != nil {
		t.Fatalf("dial proxy failed: %v", err)
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	resp, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check through proxy failed: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("expected SERVING, got %s", resp.GetStatus().String())
	}
}

func TestProxyServerTripsBreakerAfterRepeatedFailures(t *testing.T) {
	// 连续失败达到阈值后，代理应直接在本地断路，不再继续放行请求。
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}

	server := New(&fakeResolver{
		instances: nil,
	}, &staticPicker{})

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(listener)
	}()
	defer func() {
		_ = server.Shutdown()
		select {
		case serveErr := <-errCh:
			if serveErr != nil {
				t.Fatalf("proxy serve failed: %v", serveErr)
			}
		case <-time.After(time.Second):
			t.Fatal("proxy server did not stop in time")
		}
	}()

	conn, err := grpc.DialContext(
		context.Background(),
		listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithAuthority("auth.default.svc.cluster.local:9090"),
	)
	if err != nil {
		t.Fatalf("dial proxy failed: %v", err)
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	for i := 0; i < 10; i++ {
		_, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
		if err == nil {
			t.Fatalf("expected failure at round %d", i+1)
		}
	}

	_, err = client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err == nil {
		t.Fatal("expected breaker open error")
	}
	if got := err.Error(); got == "" || !containsAll(got, []string{"Unavailable", "circuit breaker open"}) {
		t.Fatalf("expected breaker open error, got %v", err)
	}
}

func startHealthBackend(t *testing.T) (string, func()) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backend listen failed: %v", err)
	}

	grpcServer := grpc.NewServer()
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcServer, healthServer)

	go func() {
		_ = grpcServer.Serve(listener)
	}()

	return listener.Addr().String(), func() {
		grpcServer.Stop()
		_ = listener.Close()
	}
}

func containsAll(value string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
