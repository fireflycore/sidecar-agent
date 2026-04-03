package config

import "testing"

func TestDefaultConfigIsValid(t *testing.T) {
	// 默认配置用于本地快速启动骨架，因此必须始终可通过校验。
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
}
