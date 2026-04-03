package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 是 V3 sidecar-agent 的最小配置模型。
//
// 当前阶段严格对齐 mesh-agent-plan.md 中给出的字段，
// 不额外扩展控制面、观测或其他运行时选项。
type Config struct {
	// ClusterName 表示当前 agent 所属集群名称，例如 cluster-a。
	ClusterName string `yaml:"cluster_name"`
	// AgentPort 是本地代理入口端口，当前规划固定为 15001。
	AgentPort int `yaml:"agent_port"`
	// DNSPort 是本地 DNS 服务监听端口，默认按规划使用 53。
	DNSPort int `yaml:"dns_port"`
	// UpstreamDNS 是非 svc.cluster.local 域名的上游 DNS 地址。
	UpstreamDNS string `yaml:"upstream_dns"`
	// ConsulAddr 是本地或近端 Consul HTTP API 地址。
	ConsulAddr string `yaml:"consul_addr"`
	// HostIP 是当前主机用于后续服务注册的地址。
	HostIP string `yaml:"host_ip"`
}

// Default 返回 V3 当前阶段的默认配置。
func Default() Config {
	return Config{
		ClusterName: "cluster-a",
		AgentPort:   15001,
		DNSPort:     53,
		UpstreamDNS: "8.8.8.8:53",
		ConsulAddr:  "127.0.0.1:8500",
		HostIP:      "127.0.0.1",
	}
}

// Load 从 YAML 文件加载配置。
//
// 当 path 为空时，直接返回默认配置，便于先跑通骨架。
func Load(path string) (Config, error) {
	cfg := Default()
	if strings.TrimSpace(path) == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 校验当前阶段真正会被 sidecar-agent 使用的关键字段。
func (c Config) Validate() error {
	if strings.TrimSpace(c.ClusterName) == "" {
		return errors.New("cluster_name is required")
	}
	if c.AgentPort <= 0 || c.AgentPort > 65535 {
		return fmt.Errorf("agent_port is invalid: %d", c.AgentPort)
	}
	if c.DNSPort <= 0 || c.DNSPort > 65535 {
		return fmt.Errorf("dns_port is invalid: %d", c.DNSPort)
	}
	if strings.TrimSpace(c.UpstreamDNS) == "" {
		return errors.New("upstream_dns is required")
	}
	if strings.TrimSpace(c.ConsulAddr) == "" {
		return errors.New("consul_addr is required")
	}
	if strings.TrimSpace(c.HostIP) == "" {
		return errors.New("host_ip is required")
	}
	return nil
}
