package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fireflycore/sidecar-agent/config"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// System 统一管理 sidecar-agent 的 OTel 日志、指标与生命周期。
type System struct {
	// logger 表示当前进程应使用的统一日志器。
	logger *slog.Logger
	// metrics 封装当前进程暴露的指标句柄。
	metrics *Metrics
	// metricsHandler 在 Prometheus exporter 启用时提供 HTTP 暴露入口。
	metricsHandler http.Handler
	// logProvider 保存 OTel 日志 provider，便于进程退出时统一关闭。
	logProvider *sdklog.LoggerProvider
	// meterProvider 保存 OTel 指标 provider，便于进程退出时统一关闭。
	meterProvider *sdkmetric.MeterProvider
}

// New 根据 telemetry 配置安装 sidecar-agent 需要的可观测能力。
func New(ctx context.Context, cfg config.TelemetryConfig, serviceName string, attrs ...attribute.KeyValue) (*System, error) {
	// 先准备默认的本地日志器，确保即便 OTel 关闭也有日志输出。
	system := &System{
		logger: newFallbackLogger(),
	}
	// 仅当至少有一个信号开启时才初始化 OTel resource。
	if cfg.LogEnabled || cfg.MetricEnabled {
		// 给资源统一挂上 service.name 等属性，确保日志与指标归属一致。
		res, err := buildResource(ctx, serviceName, attrs...)
		if err != nil {
			return nil, err
		}
		// 根据配置安装日志 provider。
		if cfg.LogEnabled {
			logProvider, err := newLoggerProvider(ctx, cfg, res)
			if err != nil {
				return nil, err
			}
			system.logProvider = logProvider
			system.logger = otelslog.NewLogger(serviceName, otelslog.WithLoggerProvider(logProvider))
		}
		// 根据配置安装指标 provider。
		if cfg.MetricEnabled {
			meterProvider, handler, err := newMeterProvider(ctx, cfg, res)
			if err != nil {
				return nil, err
			}
			system.meterProvider = meterProvider
			system.metricsHandler = handler
			otelapi.SetMeterProvider(meterProvider)
		}
	}
	// 无论 exporter 是否开启，都创建统一的指标句柄。
	metrics, err := NewMetrics()
	if err != nil {
		return nil, err
	}
	system.metrics = metrics
	// 返回组装完成的 telemetry system。
	return system, nil
}

// Logger 返回当前进程应使用的日志器。
func (s *System) Logger() *slog.Logger {
	// 返回统一日志器实例。
	return s.logger
}

// Metrics 返回统一指标句柄。
func (s *System) Metrics() *Metrics {
	// 返回当前 telemetry 管理的指标集合。
	return s.metrics
}

// MetricsHandler 返回 Prometheus exporter 对应的 HTTP 处理器。
func (s *System) MetricsHandler() http.Handler {
	// 指标未启用或采用 OTLP 导出时会返回 nil。
	return s.metricsHandler
}

// Shutdown 在进程退出时统一关闭 OTel provider。
func (s *System) Shutdown(ctx context.Context) error {
	// 逐项收集关闭错误，避免只返回第一个错误。
	var shutdownErr error
	if s.logProvider != nil {
		shutdownErr = errors.Join(shutdownErr, s.logProvider.Shutdown(ctx))
	}
	if s.meterProvider != nil {
		shutdownErr = errors.Join(shutdownErr, s.meterProvider.Shutdown(ctx))
	}
	return shutdownErr
}

// buildResource 构建日志与指标共享的 OTel resource。
func buildResource(ctx context.Context, serviceName string, attrs ...attribute.KeyValue) (*resource.Resource, error) {
	// 限定初始化超时，避免 exporter 初始化长期阻塞启动。
	initCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// 拼装 service.name 与调用方附加属性。
	resourceAttrs := append([]attribute.KeyValue{
		semconv.ServiceName(serviceName),
	}, attrs...)
	// 使用统一 resource 描述当前进程身份。
	return resource.New(
		initCtx,
		resource.WithTelemetrySDK(),
		resource.WithProcess(),
		resource.WithAttributes(resourceAttrs...),
	)
}

// newLoggerProvider 根据配置创建 OTel 日志 provider。
func newLoggerProvider(ctx context.Context, cfg config.TelemetryConfig, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	// 限定 exporter 初始化超时。
	initCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	// 根据配置选择日志 exporter。
	var exporter sdklog.Exporter
	switch strings.TrimSpace(cfg.LogExporter) {
	case "stdout":
		stdoutExporter, err := stdoutlog.New()
		if err != nil {
			return nil, err
		}
		exporter = stdoutExporter
	case "otlp":
		otlpExporter, err := otlploghttp.New(initCtx, otlploghttp.WithEndpointURL(strings.TrimSpace(cfg.OTLPEndpoint)))
		if err != nil {
			return nil, err
		}
		exporter = otlpExporter
	default:
		return nil, errors.New("unsupported telemetry log exporter")
	}
	// 使用 batch processor 承接日志导出，避免热路径同步阻塞。
	return sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(res),
	), nil
}

// newMeterProvider 根据配置创建 OTel 指标 provider。
func newMeterProvider(ctx context.Context, cfg config.TelemetryConfig, res *resource.Resource) (*sdkmetric.MeterProvider, http.Handler, error) {
	// 根据配置选择指标 exporter。
	switch strings.TrimSpace(cfg.MetricExporter) {
	case "prometheus":
		// Prometheus exporter 同时提供 Reader 与 HTTP Handler。
		exporter, err := prometheus.New()
		if err != nil {
			return nil, nil, err
		}
		return sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(exporter),
		), promhttp.Handler(), nil
	case "otlp":
		// OTLP 指标 exporter 采用周期性 reader 主动导出。
		initCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		exporter, err := otlpmetrichttp.New(initCtx, otlpmetrichttp.WithEndpointURL(strings.TrimSpace(cfg.OTLPEndpoint)))
		if err != nil {
			return nil, nil, err
		}
		return sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
		), nil, nil
	default:
		return nil, nil, errors.New("unsupported telemetry metric exporter")
	}
}
