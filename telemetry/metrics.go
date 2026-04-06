package telemetry

import (
	"context"
	"sync/atomic"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Metrics 保存当前 agent 需要暴露的最小 OTel 指标句柄。
type Metrics struct {
	// registerTotal 统计成功注册次数。
	registerTotal metric.Int64Counter
	// drainTotal 统计摘流次数。
	drainTotal metric.Int64Counter
	// deregisterTotal 统计注销次数。
	deregisterTotal metric.Int64Counter
	// discoveryRefreshTotal 统计发现刷新次数。
	discoveryRefreshTotal metric.Int64Counter
	// xdsPublishTotal 统计快照发布时间数。
	xdsPublishTotal metric.Int64Counter
	// envoyRestartTotal 统计 Envoy 重启次数。
	envoyRestartTotal metric.Int64Counter
	// localServiceGaugeValue 保存当前本机服务数量。
	localServiceGaugeValue atomic.Int64
	// callbacks 保存 observable instrument 的回调注册，避免被 GC 提前回收。
	callbacks []metric.Registration
}

// NewMetrics 创建一个基于全局 OTel meter provider 的指标容器。
func NewMetrics() (*Metrics, error) {
	// 所有 sidecar-agent 指标统一挂在同一个 meter 下。
	meter := otelapi.Meter("github.com/fireflycore/sidecar-agent/telemetry")
	// 先创建基础计数器。
	registerTotal, err := meter.Int64Counter("sidecar_agent.register.total")
	if err != nil {
		return nil, err
	}
	drainTotal, err := meter.Int64Counter("sidecar_agent.drain.total")
	if err != nil {
		return nil, err
	}
	deregisterTotal, err := meter.Int64Counter("sidecar_agent.deregister.total")
	if err != nil {
		return nil, err
	}
	discoveryRefreshTotal, err := meter.Int64Counter("sidecar_agent.discovery.refresh.total")
	if err != nil {
		return nil, err
	}
	xdsPublishTotal, err := meter.Int64Counter("sidecar_agent.xds.publish.total")
	if err != nil {
		return nil, err
	}
	envoyRestartTotal, err := meter.Int64Counter("sidecar_agent.envoy.restart.total")
	if err != nil {
		return nil, err
	}
	// 再创建本机服务数量 observable gauge。
	localServiceGauge, err := meter.Int64ObservableGauge("sidecar_agent.local.services")
	if err != nil {
		return nil, err
	}
	// 先组装 metrics 对象，供回调闭包读取当前 gauge 值。
	metrics := &Metrics{
		registerTotal:         registerTotal,
		drainTotal:            drainTotal,
		deregisterTotal:       deregisterTotal,
		discoveryRefreshTotal: discoveryRefreshTotal,
		xdsPublishTotal:       xdsPublishTotal,
		envoyRestartTotal:     envoyRestartTotal,
	}
	// 注册 gauge 回调，让 Prometheus 或 OTLP 导出时都从统一值读取。
	registration, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		observer.ObserveInt64(localServiceGauge, metrics.localServiceGaugeValue.Load())
		return nil
	}, localServiceGauge)
	if err != nil {
		return nil, err
	}
	metrics.callbacks = append(metrics.callbacks, registration)
	// 返回初始化完成的指标对象。
	return metrics, nil
}

// IncRegister 增加注册计数。
func (m *Metrics) IncRegister() {
	// 注册成功后计数加一。
	m.registerTotal.Add(context.Background(), 1)
}

// IncDrain 增加摘流计数。
func (m *Metrics) IncDrain() {
	// 摘流成功后计数加一。
	m.drainTotal.Add(context.Background(), 1)
}

// IncDeregister 增加注销计数。
func (m *Metrics) IncDeregister() {
	// 注销成功后计数加一。
	m.deregisterTotal.Add(context.Background(), 1)
}

// IncDiscoveryRefresh 增加发现刷新计数。
func (m *Metrics) IncDiscoveryRefresh() {
	// 每次完成一轮发现刷新后都累计一次。
	m.discoveryRefreshTotal.Add(context.Background(), 1)
}

// IncXDSPublish 增加 xDS 发布计数。
func (m *Metrics) IncXDSPublish() {
	// 每发布一版快照就增加一次。
	m.xdsPublishTotal.Add(context.Background(), 1)
}

// IncEnvoyRestart 增加 Envoy 异常重启计数。
func (m *Metrics) IncEnvoyRestart() {
	// 仅在异常退出后重拉起时增加。
	m.envoyRestartTotal.Add(context.Background(), 1)
}

// SetLocalServiceGauge 设置本机服务数量。
func (m *Metrics) SetLocalServiceGauge(value int) {
	// 使用整型 gauge 便于直接暴露当前值。
	m.localServiceGaugeValue.Store(int64(value))
}
