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
