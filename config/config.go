package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 描述 sidecar-agent v2.1 骨架在本期真正启用的配置项。
type Config struct {
	// Env 表示当前 agent 所属环境。
	Env string `yaml:"env"`
	// ClusterName 表示当前宿主机所在集群。
	ClusterName string `yaml:"cluster_name"`
	// Zone 表示宿主机所属机房或可用区。
	Zone string `yaml:"zone"`
	// HostIP 表示写入 Consul 的主机地址。
	HostIP string `yaml:"host_ip"`
	// DNS 保存本地 DNS 模块配置。
	DNS DNSConfig `yaml:"dns"`
	// Admin 保存本地管理接口配置。
	Admin AdminConfig `yaml:"admin"`
	// Discovery 保存 Consul 发现刷新参数。
	Discovery DiscoveryConfig `yaml:"discovery"`
	// Consul 保存注册中心参数。
	Consul ConsulConfig `yaml:"consul"`
	// XDS 保存 xDS 监听与治理基线。
	XDS XDSConfig `yaml:"xds"`
	// Envoy 保存共享 Envoy 管理参数。
	Envoy EnvoyConfig `yaml:"envoy"`
	// Telemetry 保存 OTel 日志与指标配置。
	Telemetry TelemetryConfig `yaml:"telemetry"`
}

// DNSConfig 描述本地 DNS 监听参数。
type DNSConfig struct {
	// ListenAddress 表示本地 DNS 服务监听地址。
	ListenAddress string `yaml:"listen_address"`
	// UpstreamDNS 表示非 mesh 域名的上游解析器地址。
	UpstreamDNS string `yaml:"upstream_dns"`
}

// AdminConfig 描述本地管理接口配置。
type AdminConfig struct {
	// ListenAddress 表示本地管理接口监听地址。
	ListenAddress string `yaml:"listen_address"`
	// EnableDebug 控制调试接口是否打开。
	EnableDebug bool `yaml:"enable_debug"`
}

// DiscoveryConfig 描述 Consul 拉取节奏。
type DiscoveryConfig struct {
	// RefreshInterval 表示轮询刷新间隔。
	RefreshInterval string `yaml:"refresh_interval"`
	// DebounceInterval 表示连续变更时的最小发布间隔。
	DebounceInterval string `yaml:"debounce_interval"`
}

// ConsulConfig 描述注册中心访问参数。
type ConsulConfig struct {
	// Address 表示 Consul HTTP API 地址。
	Address string `yaml:"address"`
	// Scheme 表示访问协议。
	Scheme string `yaml:"scheme"`
	// Datacenter 表示默认数据中心。
	Datacenter string `yaml:"datacenter"`
	// RouteKVPrefix 表示完整路由文档写入前缀。
	RouteKVPrefix string `yaml:"route_kv_prefix"`
}

// XDSConfig 描述 xDS 监听与治理默认值。
type XDSConfig struct {
	// ListenAddress 表示 xDS gRPC 服务监听地址。
	ListenAddress string `yaml:"listen_address"`
	// NodeID 表示 Envoy 拉取配置时使用的节点 ID。
	NodeID string `yaml:"node_id"`
	// ListenerAddress 表示共享 Envoy 数据入口监听地址。
	ListenerAddress string `yaml:"listener_address"`
	// RouteName 表示 RDS 资源名称。
	RouteName string `yaml:"route_name"`
	// DefaultTimeout 表示默认请求超时。
	DefaultTimeout string `yaml:"default_timeout"`
	// RetryAttempts 表示默认最大重试次数。
	RetryAttempts uint32 `yaml:"retry_attempts"`
	// PerTryTimeout 表示单次重试超时。
	PerTryTimeout string `yaml:"per_try_timeout"`
	// MaxConnections 表示默认最大连接数。
	MaxConnections uint32 `yaml:"max_connections"`
	// MaxPendingRequests 表示默认最大排队请求数。
	MaxPendingRequests uint32 `yaml:"max_pending_requests"`
	// MaxRequests 表示默认最大并发请求数。
	MaxRequests uint32 `yaml:"max_requests"`
	// Consecutive5XX 表示异常剔除触发阈值。
	Consecutive5XX uint32 `yaml:"consecutive_5xx"`
	// BaseEjectionTime 表示异常剔除基础时长。
	BaseEjectionTime string `yaml:"base_ejection_time"`
	// MaxEjectionPercent 表示最大剔除比例。
	MaxEjectionPercent uint32 `yaml:"max_ejection_percent"`
}

// EnvoyConfig 描述共享 Envoy 生命周期管理参数。
type EnvoyConfig struct {
	// Enabled 控制是否由 agent 自动拉起 Envoy。
	Enabled bool `yaml:"enabled"`
	// BinaryPath 表示 Envoy 可执行文件路径。
	BinaryPath string `yaml:"binary_path"`
	// BootstrapPath 表示 agent 生成的 bootstrap 文件位置。
	BootstrapPath string `yaml:"bootstrap_path"`
	// AdminAddress 表示 Envoy admin 地址。
	AdminAddress string `yaml:"admin_address"`
	// DrainTimeout 表示停止 Envoy 时的优雅等待时长。
	DrainTimeout string `yaml:"drain_timeout"`
	// RestartBackoff 表示异常重启前等待时间。
	RestartBackoff string `yaml:"restart_backoff"`
	// LogLevel 表示 Envoy 日志级别。
	LogLevel string `yaml:"log_level"`
}

// TelemetryConfig 描述 OTel 日志与指标导出配置。
type TelemetryConfig struct {
	// OTLPEndpoint 表示 OTLP/HTTP 导出端点。
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	// MetricEnabled 控制是否启用 OTel 指标。
	MetricEnabled bool `yaml:"metric_enabled"`
	// MetricExporter 控制指标导出方式，当前支持 prometheus 与 otlp。
	MetricExporter string `yaml:"metric_exporter"`
	// LogEnabled 控制是否启用 OTel 日志。
	LogEnabled bool `yaml:"log_enabled"`
	// LogExporter 控制日志导出方式，当前支持 stdout 与 otlp。
	LogExporter string `yaml:"log_exporter"`
}

// Default 返回严格对齐 v2.1 与 plan-v2.1 的最小默认配置。
func Default() Config {
	// 这里默认采用开发环境参数，方便先本机跑通最小链路。
	return Config{
		Env:         "dev",
		ClusterName: "cluster-a",
		Zone:        "idc-a-1",
		HostIP:      "127.0.0.1",
		DNS: DNSConfig{
			// DNS 只在本机监听，符合文档中的本地安全边界。
			ListenAddress: "127.0.0.1:15353",
			// 非 mesh 域名继续转发给上游 DNS。
			UpstreamDNS: "8.8.8.8:53",
		},
		Admin: AdminConfig{
			// 管理接口固定落在本机 15010。
			ListenAddress: "127.0.0.1:15010",
			// 默认打开调试接口，便于开发阶段观察。
			EnableDebug: true,
		},
		Discovery: DiscoveryConfig{
			// 轮询间隔使用 5 秒，兼顾实现简单与可观测性。
			RefreshInterval: "5s",
			// 连续变更时最小发布间隔使用 1 秒，避免频繁抖动。
			DebounceInterval: "1s",
		},
		Consul: ConsulConfig{
			// Consul 默认走本机 HTTP API。
			Address: "127.0.0.1:8500",
			// 默认协议为 http。
			Scheme: "http",
			// 默认数据中心名采用 dc1。
			Datacenter: "dc1",
			// 路由文档前缀严格使用 routes。
			RouteKVPrefix: "routes",
		},
		XDS: XDSConfig{
			// xDS 按文档监听 15011。
			ListenAddress: "127.0.0.1:15011",
			// NodeID 使用固定可读值，便于调试。
			NodeID: "sidecar-agent-dev-node",
			// 共享 Envoy 数据入口按文档监听 15001。
			ListenerAddress: "127.0.0.1:15001",
			// RDS 资源名保持单 listener 单 route 的稳定命名。
			RouteName: "local-route",
			// 默认超时严格对齐文档基线 3 秒。
			DefaultTimeout: "3s",
			// 幂等请求默认最多重试 2 次。
			RetryAttempts: 2,
			// 单次重试超时默认 1 秒。
			PerTryTimeout: "1s",
			// 以下熔断基线保持保守值，避免共享 Envoy 被瞬时流量拖垮。
			MaxConnections:     1024,
			MaxPendingRequests: 2048,
			MaxRequests:        4096,
			Consecutive5XX:     5,
			BaseEjectionTime:   "30s",
			MaxEjectionPercent: 50,
		},
		Envoy: EnvoyConfig{
			// 默认关闭自动拉起，避免缺少 Envoy 二进制时影响最小联调。
			Enabled: false,
			// 二进制名默认使用 PATH 中的 envoy。
			BinaryPath: "envoy",
			// bootstrap 默认写入工作目录下的临时文件。
			BootstrapPath: "./.tmp/envoy-bootstrap.yaml",
			// admin 接口固定落在本机 19000。
			AdminAddress: "127.0.0.1:19000",
			// 停止时默认等待 20 秒。
			DrainTimeout: "20s",
			// 异常退出后等待 3 秒再重启。
			RestartBackoff: "3s",
			// 默认日志级别采用 info。
			LogLevel: "info",
		},
		Telemetry: TelemetryConfig{
			// OTLP 端点默认指向本机 Collector。
			OTLPEndpoint: "http://127.0.0.1:4318",
			// 指标默认开启。
			MetricEnabled: true,
			// 默认通过 Prometheus exporter 暴露本机指标。
			MetricExporter: "prometheus",
			// 日志默认开启。
			LogEnabled: true,
			// 默认把日志通过 OTel stdout exporter 输出到标准输出，便于本机联调。
			LogExporter: "stdout",
		},
	}
}

// Load 从 YAML 文件加载配置，未传路径时直接返回默认值。
func Load(path string) (Config, error) {
	// 先以默认值为基础，确保缺省字段也有稳定语义。
	cfg := Default()
	// 未传路径时直接返回默认配置。
	if strings.TrimSpace(path) == "" {
		// 默认配置同样要经过校验，保证没有隐藏问题。
		return cfg, cfg.Validate()
	}
	// 读取用户提供的 YAML 文件。
	data, err := os.ReadFile(path)
	if err != nil {
		// 读取失败时直接返回底层错误。
		return Config{}, err
	}
	// 反序列化到默认配置之上，支持局部覆盖。
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		// YAML 非法时终止加载。
		return Config{}, err
	}
	// 当 bootstrap 路径是相对路径时，转换成相对于配置文件目录的绝对路径。
	if strings.TrimSpace(cfg.Envoy.BootstrapPath) != "" && !filepath.IsAbs(cfg.Envoy.BootstrapPath) {
		// 先计算配置文件所在目录。
		baseDir := filepath.Dir(path)
		// 把相对路径转成稳定绝对路径。
		cfg.Envoy.BootstrapPath = filepath.Join(baseDir, cfg.Envoy.BootstrapPath)
	}
	// 返回校验后的配置。
	return cfg, cfg.Validate()
}

// Validate 校验所有会参与运行时装配的关键参数。
func (c Config) Validate() error {
	// 环境不能为空。
	if strings.TrimSpace(c.Env) == "" {
		return errors.New("env is required")
	}
	// 集群名不能为空。
	if strings.TrimSpace(c.ClusterName) == "" {
		return errors.New("cluster_name is required")
	}
	// zone 不能为空。
	if strings.TrimSpace(c.Zone) == "" {
		return errors.New("zone is required")
	}
	// host_ip 必须是合法 IP。
	if net.ParseIP(strings.TrimSpace(c.HostIP)) == nil {
		return fmt.Errorf("host_ip is invalid: %s", c.HostIP)
	}
	// 地址类字段统一校验 host:port 格式。
	addresses := map[string]string{
		"dns.listen_address":   c.DNS.ListenAddress,
		"admin.listen_address": c.Admin.ListenAddress,
		"xds.listen_address":   c.XDS.ListenAddress,
		"xds.listener_address": c.XDS.ListenerAddress,
		"envoy.admin_address":  c.Envoy.AdminAddress,
		"dns.upstream_dns":     c.DNS.UpstreamDNS,
	}
	// 遍历所有地址字段执行校验。
	for field, value := range addresses {
		if err := validateAddress(field, value); err != nil {
			return err
		}
	}
	// Consul 地址是 host:port，不允许为空。
	if err := validateAddress("consul.address", c.Consul.Address); err != nil {
		return err
	}
	// 路由前缀必须存在。
	if strings.TrimSpace(c.Consul.RouteKVPrefix) == "" {
		return errors.New("consul.route_kv_prefix is required")
	}
	// 关键字符串字段需要非空。
	requiredStrings := map[string]string{
		"consul.scheme":             c.Consul.Scheme,
		"consul.datacenter":         c.Consul.Datacenter,
		"xds.node_id":               c.XDS.NodeID,
		"xds.route_name":            c.XDS.RouteName,
		"xds.default_timeout":       c.XDS.DefaultTimeout,
		"xds.per_try_timeout":       c.XDS.PerTryTimeout,
		"envoy.drain_timeout":       c.Envoy.DrainTimeout,
		"envoy.restart_backoff":     c.Envoy.RestartBackoff,
		"envoy.log_level":           c.Envoy.LogLevel,
		"telemetry.metric_exporter": c.Telemetry.MetricExporter,
		"telemetry.log_exporter":    c.Telemetry.LogExporter,
	}
	// 逐项校验必填字符串。
	for field, value := range requiredStrings {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", field)
		}
	}
	// 轮询与治理类时长必须可解析。
	durations := map[string]string{
		"discovery.refresh_interval":  c.Discovery.RefreshInterval,
		"discovery.debounce_interval": c.Discovery.DebounceInterval,
		"xds.default_timeout":         c.XDS.DefaultTimeout,
		"xds.per_try_timeout":         c.XDS.PerTryTimeout,
		"xds.base_ejection_time":      c.XDS.BaseEjectionTime,
		"envoy.drain_timeout":         c.Envoy.DrainTimeout,
		"envoy.restart_backoff":       c.Envoy.RestartBackoff,
	}
	// 逐项解析所有时长字段。
	for field, value := range durations {
		if _, err := time.ParseDuration(strings.TrimSpace(value)); err != nil {
			return fmt.Errorf("%s is invalid: %w", field, err)
		}
	}
	// 自动拉起 Envoy 时需要二进制路径和 bootstrap 路径。
	if c.Envoy.Enabled {
		if strings.TrimSpace(c.Envoy.BinaryPath) == "" {
			return errors.New("envoy.binary_path is required when envoy.enabled is true")
		}
		if strings.TrimSpace(c.Envoy.BootstrapPath) == "" {
			return errors.New("envoy.bootstrap_path is required when envoy.enabled is true")
		}
	}
	// telemetry.metric_exporter 仅允许已知导出器，避免拼写错误导致静默失效。
	switch strings.TrimSpace(c.Telemetry.MetricExporter) {
	case "prometheus", "otlp":
	default:
		return fmt.Errorf("telemetry.metric_exporter is invalid: %s", c.Telemetry.MetricExporter)
	}
	// telemetry.log_exporter 仅允许已知导出器。
	switch strings.TrimSpace(c.Telemetry.LogExporter) {
	case "stdout", "otlp":
	default:
		return fmt.Errorf("telemetry.log_exporter is invalid: %s", c.Telemetry.LogExporter)
	}
	// 当任一信号使用 OTLP exporter 时，端点必须存在。
	if (c.Telemetry.MetricEnabled && strings.TrimSpace(c.Telemetry.MetricExporter) == "otlp") ||
		(c.Telemetry.LogEnabled && strings.TrimSpace(c.Telemetry.LogExporter) == "otlp") {
		if strings.TrimSpace(c.Telemetry.OTLPEndpoint) == "" {
			return errors.New("telemetry.otlp_endpoint is required when using otlp exporter")
		}
	}
	// 配置校验通过后返回 nil。
	return nil
}

// MustDuration 把配置中的时长字符串解析成 time.Duration。
func MustDuration(raw string) time.Duration {
	// 这里复用 Validate 保障过的合法性，所以解析失败视为编程错误。
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		panic(err)
	}
	// 返回解析结果。
	return value
}

// validateAddress 校验通用的 host:port 地址字符串。
func validateAddress(field, value string) error {
	// 地址不能为空。
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	// SplitHostPort 能够同时校验端口与地址结构。
	if _, _, err := net.SplitHostPort(strings.TrimSpace(value)); err != nil {
		return fmt.Errorf("%s is invalid: %w", field, err)
	}
	// 地址合法时返回 nil。
	return nil
}
