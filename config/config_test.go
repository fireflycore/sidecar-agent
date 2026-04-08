package config

import "testing"

// TestDefaultValidate 验证默认配置能够直接通过校验。
func TestDefaultValidate(t *testing.T) {
	// 读取默认配置。
	cfg := Default()
	// 默认值应当可直接用于最小骨架启动。
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
}

// TestValidateRejectsInvalidHostIP 验证非法 host_ip 会被拒绝。
func TestValidateRejectsInvalidHostIP(t *testing.T) {
	// 基于默认配置构造一个非法 IP 场景。
	cfg := Default()
	// 把 host_ip 改成非法字符串。
	cfg.HostIP = "not-an-ip"
	// 校验必须失败。
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid host_ip to fail")
	}
}

// TestValidateRejectsMissingTraceEndpoint 验证 trace 走 OTLP 时必须提供端点。
func TestValidateRejectsMissingTraceEndpoint(t *testing.T) {
	// 基于默认配置构造 trace OTLP 场景。
	cfg := Default()
	// 打开 trace 并切到 OTLP exporter。
	cfg.Telemetry.TraceEnabled = true
	cfg.Telemetry.TraceExporter = "otlp"
	cfg.Telemetry.OTLPEndpoint = ""
	// 校验必须失败。
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing trace endpoint to fail")
	}
}

// TestValidateRejectsLeaseRefreshNotSmallerThanTTL 验证 ownership 续租间隔必须小于 TTL。
func TestValidateRejectsLeaseRefreshNotSmallerThanTTL(t *testing.T) {
	// 基于默认配置构造一个错误的 lease 配置场景。
	cfg := Default()
	// 把续租间隔设置成大于等于 TTL 的值。
	cfg.Consul.AgentLeaseTTL = "10s"
	cfg.Consul.AgentLeaseRefreshInterval = "10s"
	// 校验必须失败。
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid lease refresh interval to fail")
	}
}

func TestValidateRejectsInvalidConsulRequestTimeout(t *testing.T) {
	cfg := Default()
	cfg.Consul.RequestTimeout = "not-a-duration"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid consul request timeout to fail")
	}
}
