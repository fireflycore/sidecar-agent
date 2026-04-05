package telemetry

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

// Metrics 保存当前 agent 需要暴露的最小 Prometheus 指标。
type Metrics struct {
	// registerTotal 统计成功注册次数。
	registerTotal atomic.Uint64
	// drainTotal 统计摘流次数。
	drainTotal atomic.Uint64
	// deregisterTotal 统计注销次数。
	deregisterTotal atomic.Uint64
	// discoveryRefreshTotal 统计发现刷新次数。
	discoveryRefreshTotal atomic.Uint64
	// xdsPublishTotal 统计快照发布时间数。
	xdsPublishTotal atomic.Uint64
	// envoyRestartTotal 统计 Envoy 重启次数。
	envoyRestartTotal atomic.Uint64
	// localServiceGauge 表示当前本机服务数量。
	localServiceGauge atomic.Int64
	// lastErrors 保存最近一次错误摘要，便于快速观察。
	lastErrors sync.Map
}

// NewMetrics 创建一个空的指标容器。
func NewMetrics() *Metrics {
	// 直接返回零值结构即可。
	return &Metrics{}
}

// IncRegister 增加注册计数。
func (m *Metrics) IncRegister() {
	// 注册成功后计数加一。
	m.registerTotal.Add(1)
}

// IncDrain 增加摘流计数。
func (m *Metrics) IncDrain() {
	// 摘流成功后计数加一。
	m.drainTotal.Add(1)
}

// IncDeregister 增加注销计数。
func (m *Metrics) IncDeregister() {
	// 注销成功后计数加一。
	m.deregisterTotal.Add(1)
}

// IncDiscoveryRefresh 增加发现刷新计数。
func (m *Metrics) IncDiscoveryRefresh() {
	// 每次完成一轮发现刷新后都累计一次。
	m.discoveryRefreshTotal.Add(1)
}

// IncXDSPublish 增加 xDS 发布计数。
func (m *Metrics) IncXDSPublish() {
	// 每发布一版快照就增加一次。
	m.xdsPublishTotal.Add(1)
}

// IncEnvoyRestart 增加 Envoy 异常重启计数。
func (m *Metrics) IncEnvoyRestart() {
	// 仅在异常退出后重拉起时增加。
	m.envoyRestartTotal.Add(1)
}

// SetLocalServiceGauge 设置本机服务数量。
func (m *Metrics) SetLocalServiceGauge(value int) {
	// 使用整型 gauge 便于直接暴露当前值。
	m.localServiceGauge.Store(int64(value))
}

// SetLastError 记录最近一次错误摘要。
func (m *Metrics) SetLastError(component, value string) {
	// 统一用组件名作为键，便于 /metrics 输出时展开。
	m.lastErrors.Store(strings.TrimSpace(component), strings.TrimSpace(value))
}

// Handler 返回一个原生 Prometheus 文本输出处理器。
func (m *Metrics) Handler() http.Handler {
	// 返回闭包处理器，避免额外依赖。
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 指标输出使用 text/plain。
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		// 输出所有计数器与 gauge。
		fmt.Fprintf(w, "# HELP sidecar_agent_register_total 成功注册次数\n")
		fmt.Fprintf(w, "# TYPE sidecar_agent_register_total counter\n")
		fmt.Fprintf(w, "sidecar_agent_register_total %d\n", m.registerTotal.Load())
		fmt.Fprintf(w, "# HELP sidecar_agent_drain_total 成功摘流次数\n")
		fmt.Fprintf(w, "# TYPE sidecar_agent_drain_total counter\n")
		fmt.Fprintf(w, "sidecar_agent_drain_total %d\n", m.drainTotal.Load())
		fmt.Fprintf(w, "# HELP sidecar_agent_deregister_total 成功注销次数\n")
		fmt.Fprintf(w, "# TYPE sidecar_agent_deregister_total counter\n")
		fmt.Fprintf(w, "sidecar_agent_deregister_total %d\n", m.deregisterTotal.Load())
		fmt.Fprintf(w, "# HELP sidecar_agent_discovery_refresh_total 发现刷新次数\n")
		fmt.Fprintf(w, "# TYPE sidecar_agent_discovery_refresh_total counter\n")
		fmt.Fprintf(w, "sidecar_agent_discovery_refresh_total %d\n", m.discoveryRefreshTotal.Load())
		fmt.Fprintf(w, "# HELP sidecar_agent_xds_publish_total xDS 发布次数\n")
		fmt.Fprintf(w, "# TYPE sidecar_agent_xds_publish_total counter\n")
		fmt.Fprintf(w, "sidecar_agent_xds_publish_total %d\n", m.xdsPublishTotal.Load())
		fmt.Fprintf(w, "# HELP sidecar_agent_envoy_restart_total Envoy 重启次数\n")
		fmt.Fprintf(w, "# TYPE sidecar_agent_envoy_restart_total counter\n")
		fmt.Fprintf(w, "sidecar_agent_envoy_restart_total %d\n", m.envoyRestartTotal.Load())
		fmt.Fprintf(w, "# HELP sidecar_agent_local_services 当前本机服务数量\n")
		fmt.Fprintf(w, "# TYPE sidecar_agent_local_services gauge\n")
		fmt.Fprintf(w, "sidecar_agent_local_services %d\n", m.localServiceGauge.Load())
		// 逐项输出最近一次错误摘要。
		m.lastErrors.Range(func(key, value any) bool {
			// 组件名需要转换成 Prometheus label 可接受的文本。
			component, _ := key.(string)
			// 错误内容中可能包含双引号，需要做最小转义。
			lastError := strings.ReplaceAll(fmt.Sprint(value), `"`, `'`)
			// 把摘要作为 gauge 的 label 暴露，值固定为 1。
			fmt.Fprintf(w, "sidecar_agent_last_error{component=%q,message=%q} 1\n", component, lastError)
			// 继续遍历所有组件。
			return true
		})
	})
}
