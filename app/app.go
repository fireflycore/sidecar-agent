package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/fireflycore/sidecar-agent/adminapi"
	"github.com/fireflycore/sidecar-agent/config"
	"github.com/fireflycore/sidecar-agent/discovery"
	"github.com/fireflycore/sidecar-agent/dns"
	"github.com/fireflycore/sidecar-agent/envoy"
	"github.com/fireflycore/sidecar-agent/model"
	"github.com/fireflycore/sidecar-agent/registry"
	"github.com/fireflycore/sidecar-agent/telemetry"
	"github.com/fireflycore/sidecar-agent/xds"
	"go.opentelemetry.io/otel/attribute"
)

// Runner 负责装配并托管 sidecar-agent v2.1 当前阶段所有运行模块。
type Runner struct {
	// cfg 保存运行配置。
	cfg config.Config
	// logger 提供统一结构化日志。
	logger *slog.Logger
	// metrics 提供最小 Prometheus 指标。
	metrics *telemetry.Metrics
	// telemetry 保存 OTel provider 生命周期。
	telemetry *telemetry.System
	// dnsServer 提供本地 DNS 拦截能力。
	dnsServer *dns.Server
	// registryClient 提供服务注册、摘流、注销与发现能力。
	registryClient *registry.Client
	// watcher 负责定期从 Consul 拉取健康实例。
	watcher *discovery.Watcher
	// xdsServer 负责向共享 Envoy 下发快照。
	xdsServer *xds.Server
	// adminServer 负责暴露管理接口。
	adminServer *adminapi.Server
	// envoyManager 负责共享 Envoy 生命周期。
	envoyManager *envoy.Manager
	// publishMu 防止主动刷新与后台轮询同时构建快照。
	publishMu sync.Mutex
}

// New 创建一个完整装配好的 sidecar-agent 运行器。
func New(cfg config.Config) (*Runner, error) {
	// 先按配置装配 OTel 日志与指标系统。
	telemetrySystem, err := telemetry.New(context.Background(), cfg.Telemetry, "sidecar-agent",
		attribute.String("sidecar.env", cfg.Env),
		attribute.String("sidecar.cluster", cfg.ClusterName),
		attribute.String("sidecar.zone", cfg.Zone),
	)
	if err != nil {
		return nil, err
	}
	// 从 telemetry 中取出统一日志器。
	logger := telemetrySystem.Logger()
	// 从 telemetry 中取出统一指标句柄。
	metrics := telemetrySystem.Metrics()
	// 创建本地 DNS 服务器。
	dnsServer := dns.New(cfg.DNS.UpstreamDNS)
	// 创建 Consul 注册中心客户端。
	registryClient, err := registry.New(registry.Settings{
		Address:       cfg.Consul.Address,
		Scheme:        cfg.Consul.Scheme,
		Datacenter:    cfg.Consul.Datacenter,
		RouteKVPrefix: cfg.Consul.RouteKVPrefix,
		ClusterName:   cfg.ClusterName,
		Zone:          cfg.Zone,
		HostIP:        cfg.HostIP,
		Env:           cfg.Env,
	}, logger, metrics)
	if err != nil {
		return nil, err
	}
	// 校验 registry 依赖是否完整。
	if err := registryClient.EnsureUsable(); err != nil {
		return nil, err
	}
	// 创建 xDS 快照构建器。
	builder := xds.NewBuilder(xds.BuilderSettings{
		NodeID:             cfg.XDS.NodeID,
		ListenerAddress:    cfg.XDS.ListenerAddress,
		RouteName:          cfg.XDS.RouteName,
		LocalCluster:       cfg.ClusterName,
		DefaultTimeout:     config.MustDuration(cfg.XDS.DefaultTimeout),
		RetryAttempts:      cfg.XDS.RetryAttempts,
		PerTryTimeout:      config.MustDuration(cfg.XDS.PerTryTimeout),
		MaxConnections:     cfg.XDS.MaxConnections,
		MaxPendingRequests: cfg.XDS.MaxPendingRequests,
		MaxRequests:        cfg.XDS.MaxRequests,
		Consecutive5XX:     cfg.XDS.Consecutive5XX,
		BaseEjectionTime:   config.MustDuration(cfg.XDS.BaseEjectionTime),
		MaxEjectionPercent: cfg.XDS.MaxEjectionPercent,
	})
	// 创建 xDS gRPC 服务。
	xdsServer, err := xds.NewServer(builder, logger, metrics)
	if err != nil {
		return nil, err
	}
	// 创建 Consul 发现 watcher。
	watcher := discovery.New(
		registryClient,
		config.MustDuration(cfg.Discovery.RefreshInterval),
		config.MustDuration(cfg.Discovery.DebounceInterval),
		logger,
		metrics,
	)
	// 先创建运行器实例，便于把自己作为 refresher 注入管理接口。
	runner := &Runner{
		cfg:            cfg,
		logger:         logger,
		metrics:        metrics,
		telemetry:      telemetrySystem,
		dnsServer:      dnsServer,
		registryClient: registryClient,
		watcher:        watcher,
		xdsServer:      xdsServer,
	}
	// 创建管理接口服务。
	runner.adminServer = adminapi.New(
		cfg.Admin.ListenAddress,
		cfg.Admin.EnableDebug,
		registryClient,
		runner,
		xdsServer,
		telemetrySystem.MetricsHandler(),
		logger,
	)
	// 当配置开启 Envoy 托管时再创建进程管理器。
	if cfg.Envoy.Enabled {
		// 先生成 bootstrap 文件。
		if err := envoy.WriteBootstrap(cfg.Envoy.BootstrapPath, envoy.BootstrapConfig{
			NodeID:       cfg.XDS.NodeID,
			ClusterName:  cfg.ClusterName,
			AdminAddress: cfg.Envoy.AdminAddress,
			XDSTarget:    cfg.XDS.ListenAddress,
		}); err != nil {
			return nil, err
		}
		// 再创建 Envoy 管理器。
		runner.envoyManager = envoy.NewManager(
			cfg.Envoy.BinaryPath,
			cfg.Envoy.BootstrapPath,
			cfg.Envoy.AdminAddress,
			cfg.Envoy.LogLevel,
			config.MustDuration(cfg.Envoy.RestartBackoff),
			logger,
			metrics,
		)
	}
	// 返回装配完成的运行器。
	return runner, nil
}

// Start 启动 sidecar-agent 的所有模块。
func (r *Runner) Start(ctx context.Context) error {
	// 先启动 xDS，确保 Envoy 拉起前控制面已就绪。
	if err := r.xdsServer.Start(r.cfg.XDS.ListenAddress); err != nil {
		return err
	}
	// 发布首版空快照，保证 Envoy 即使先连接也能拿到合法配置。
	if _, err := r.xdsServer.Publish(ctx, []model.ServiceInstance{}); err != nil {
		return err
	}
	// 若启用了 Envoy 托管，则在 xDS 就绪后再启动 Envoy。
	if r.envoyManager != nil {
		if err := r.envoyManager.Start(ctx); err != nil {
			return err
		}
	}
	// 启动本地 DNS 服务。
	go func() {
		if err := r.dnsServer.Start(r.cfg.DNS.ListenAddress); err != nil && r.logger != nil {
			r.logger.Error("dns server exited",
				slog.String("error", err.Error()),
			)
		}
	}()
	// 启动管理接口服务。
	go func() {
		if err := r.adminServer.Start(); err != nil && r.logger != nil {
			r.logger.Error("admin api exited",
				slog.String("error", err.Error()),
			)
		}
	}()
	// 启动后台发现刷新循环。
	go func() {
		if err := r.watcher.Run(ctx, r.publishInstances); err != nil && r.logger != nil {
			r.logger.Error("discovery watcher exited",
				slog.String("error", err.Error()),
			)
		}
	}()
	// 输出整体启动日志。
	if r.logger != nil {
		r.logger.Info("sidecar-agent started",
			slog.String("env", r.cfg.Env),
			slog.String("cluster", r.cfg.ClusterName),
			slog.String("zone", r.cfg.Zone),
		)
	}
	// 启动流程完成。
	return nil
}

// Shutdown 依次关闭 sidecar-agent 模块。
func (r *Runner) Shutdown(ctx context.Context) error {
	// 优先停止管理接口，避免关闭过程中再接新请求。
	if r.adminServer != nil {
		if err := r.adminServer.Shutdown(ctx); err != nil {
			return err
		}
	}
	// 停止 DNS 服务。
	if r.dnsServer != nil {
		if err := r.dnsServer.Shutdown(ctx); err != nil {
			return err
		}
	}
	// 停止 xDS 服务。
	if r.xdsServer != nil {
		r.xdsServer.Stop()
	}
	// 最后停止共享 Envoy。
	if r.envoyManager != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, config.MustDuration(r.cfg.Envoy.DrainTimeout))
		defer cancel()
		if err := r.envoyManager.Stop(shutdownCtx); err != nil {
			return err
		}
	}
	// 在所有业务模块停止后再关闭 OTel provider，确保尾部日志与指标能刷出。
	if r.telemetry != nil {
		if err := r.telemetry.Shutdown(ctx); err != nil {
			return err
		}
	}
	// 关闭完成。
	return nil
}

// RefreshNow 实现管理接口需要的主动刷新能力。
func (r *Runner) RefreshNow(ctx context.Context) error {
	// 直接调用 watcher 的主动刷新入口。
	return r.watcher.RefreshNow(ctx, r.publishInstances)
}

// publishInstances 把发现到的实例集合转换成 xDS 快照。
func (r *Runner) publishInstances(instances []model.ServiceInstance) error {
	// 发布流程串行化，避免并发构建快照。
	r.publishMu.Lock()
	defer r.publishMu.Unlock()
	// 每次发布前都更新本机服务数量指标。
	r.metrics.SetLocalServiceGauge(len(r.registryClient.LocalServices()))
	// 调用 xDS 发布最新快照。
	_, err := r.xdsServer.Publish(context.Background(), instances)
	if err != nil {
		r.metrics.SetLastError("xds", err.Error())
		return err
	}
	// 发布成功时返回 nil。
	return nil
}

// WaitForShutdown 在传入上下文结束时触发统一关闭。
func (r *Runner) WaitForShutdown(ctx context.Context) error {
	// 阻塞等待退出信号。
	<-ctx.Done()
	// 使用固定超时上下文执行关闭。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 调用统一关闭流程。
	return r.Shutdown(shutdownCtx)
}
