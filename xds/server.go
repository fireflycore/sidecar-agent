package xds

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	clustergrpc "github.com/envoyproxy/go-control-plane/envoy/service/cluster/v3"
	discoverygrpc "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	endpointgrpc "github.com/envoyproxy/go-control-plane/envoy/service/endpoint/v3"
	listenergrpc "github.com/envoyproxy/go-control-plane/envoy/service/listener/v3"
	routegrpc "github.com/envoyproxy/go-control-plane/envoy/service/route/v3"
	cachev3 "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	serverv3 "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"github.com/fireflycore/sidecar-agent/model"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/grpc"
)

// MetricsRecorder 抽象最小 xDS 指标上报能力。
type MetricsRecorder interface {
	// IncXDSPublish 统计快照发布次数。
	IncXDSPublish()
}

// Server 负责托管 xDS gRPC 服务并持有当前快照缓存。
type Server struct {
	// nodeID 表示快照对应的 Envoy 节点标识。
	nodeID string
	// builder 负责把实例集合转换成快照。
	builder *Builder
	// cache 保存当前 node 对应的快照。
	cache cachev3.SnapshotCache
	// xdsServer 是 go-control-plane 提供的 gRPC 服务实现。
	xdsServer serverv3.Server
	// grpcServer 是实际监听端口的 gRPC server。
	grpcServer *grpc.Server
	// listener 保存底层网络监听器。
	listener net.Listener
	// logger 负责输出结构化日志。
	logger *slog.Logger
	// metrics 负责累计发布指标。
	metrics MetricsRecorder
	// mu 保护 start/stop 与 summary 状态。
	mu sync.Mutex
	// started 表示当前 gRPC 服务是否已经启动。
	started bool
}

// NewServer 创建一个新的 xDS 服务实例。
func NewServer(builder *Builder, logger *slog.Logger, metrics MetricsRecorder) (*Server, error) {
	// builder 不能为空，否则无法构建快照。
	if builder == nil {
		return nil, fmt.Errorf("xds builder is required")
	}
	// 创建官方 SnapshotCache。
	cache := cachev3.NewSnapshotCache(true, cachev3.IDHash{}, nil)
	// 基于 cache 创建官方 xDS server。
	xdsServer := serverv3.NewServer(context.Background(), cache, nil)
	// 返回封装后的服务实例。
	return &Server{
		nodeID:    builder.settings.NodeID,
		builder:   builder,
		cache:     cache,
		xdsServer: xdsServer,
		logger:    logger,
		metrics:   metrics,
	}, nil
}

// Start 启动 xDS gRPC 监听。
func (s *Server) Start(listenAddress string) error {
	// 加锁避免重复启动。
	s.mu.Lock()
	defer s.mu.Unlock()
	// 已启动时直接返回。
	if s.started {
		return nil
	}
	// 监听本地 TCP 端口。
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return err
	}
	// 创建原生 gRPC server。
	grpcServer := grpc.NewServer()
	// 同时注册 ADS 与各个独立 xDS 服务，便于调试与兼容。
	discoverygrpc.RegisterAggregatedDiscoveryServiceServer(grpcServer, s.xdsServer)
	endpointgrpc.RegisterEndpointDiscoveryServiceServer(grpcServer, s.xdsServer)
	clustergrpc.RegisterClusterDiscoveryServiceServer(grpcServer, s.xdsServer)
	routegrpc.RegisterRouteDiscoveryServiceServer(grpcServer, s.xdsServer)
	listenergrpc.RegisterListenerDiscoveryServiceServer(grpcServer, s.xdsServer)
	// 保存运行态对象。
	s.listener = listener
	s.grpcServer = grpcServer
	s.started = true
	// 后台启动 gRPC 服务。
	go func() {
		if serveErr := grpcServer.Serve(listener); serveErr != nil && s.logger != nil {
			s.logger.Error("xds grpc server exited",
				slog.String("error", serveErr.Error()),
			)
		}
	}()
	// 输出启动日志。
	if s.logger != nil {
		s.logger.Info("xds grpc server started",
			slog.String("address", listenAddress),
			slog.String("node_id", s.nodeID),
		)
	}
	// 启动成功后返回 nil。
	return nil
}

// Stop 优雅停止 xDS 服务。
func (s *Server) Stop() {
	// 加锁保护关闭流程。
	s.mu.Lock()
	defer s.mu.Unlock()
	// 未启动时直接返回。
	if !s.started {
		return
	}
	// 优雅停止 gRPC 服务。
	s.grpcServer.GracefulStop()
	// 关闭底层监听器。
	_ = s.listener.Close()
	// 清理运行态标记。
	s.started = false
}

// Publish 用最新实例集合生成并发布一版快照。
func (s *Server) Publish(ctx context.Context, instances []model.ServiceInstance) (Summary, error) {
	// 为每次快照发布创建 span，便于观察发现结果到 xDS 的转换链路。
	ctx, span := otel.Tracer("sidecar-agent/xds").Start(ctx, "xds.publish")
	defer span.End()
	span.SetAttributes(attribute.Int("instances.count", len(instances)))
	// 先构建快照。
	snapshot, summary, err := s.builder.Build(instances)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return Summary{}, err
	}
	// 把快照写入 SnapshotCache。
	if err := s.cache.SetSnapshot(ctx, s.nodeID, snapshot); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return Summary{}, err
	}
	// 统计发布次数。
	if s.metrics != nil {
		s.metrics.IncXDSPublish()
	}
	// 输出发布日志。
	if s.logger != nil {
		s.logger.Info("xds snapshot published",
			slog.Uint64("version", summary.Version),
			slog.Int("clusters", summary.ClusterCount),
			slog.Int("endpoints", summary.EndpointCount),
		)
	}
	span.SetAttributes(
		attribute.Int("clusters.count", summary.ClusterCount),
		attribute.Int("endpoints.count", summary.EndpointCount),
	)
	span.SetStatus(codes.Ok, "published")
	// 返回当前摘要。
	return summary, nil
}

// Summary 返回当前已发布快照的调试摘要。
func (s *Server) Summary() Summary {
	// 直接复用 builder 中维护的摘要。
	return s.builder.Summary()
}
