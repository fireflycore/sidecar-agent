package envoy

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// BootstrapConfig 描述生成 Envoy bootstrap 文件所需的最小参数。
type BootstrapConfig struct {
	// NodeID 表示 Envoy 节点标识。
	NodeID string
	// ClusterName 表示 Envoy 自身所在集群。
	ClusterName string
	// AdminAddress 表示 Envoy admin 监听地址。
	AdminAddress string
	// XDSTarget 表示本地 xDS gRPC 服务地址。
	XDSTarget string
}

// WriteBootstrap 生成并写入最小可用的 Envoy bootstrap YAML。
func WriteBootstrap(path string, config BootstrapConfig) error {
	// 先确保目标目录存在。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 生成 bootstrap 文档结构。
	document := map[string]any{
		"node": map[string]any{
			// node id 与 cluster 会直接影响 xDS 标识。
			"id":      strings.TrimSpace(config.NodeID),
			"cluster": strings.TrimSpace(config.ClusterName),
		},
		"dynamic_resources": map[string]any{
			// CDS/RDS 都通过 ADS 从本地 agent 拉取。
			"ads_config": map[string]any{
				"api_type":              "GRPC",
				"transport_api_version": "V3",
				"grpc_services": []any{
					map[string]any{
						"envoy_grpc": map[string]any{
							"cluster_name": "xds_cluster",
						},
					},
				},
			},
			// LDS 与 CDS 也统一走 ADS。
			"lds_config": map[string]any{
				"ads":                  map[string]any{},
				"resource_api_version": "V3",
			},
			"cds_config": map[string]any{
				"ads":                  map[string]any{},
				"resource_api_version": "V3",
			},
		},
		"static_resources": map[string]any{
			// 仅保留一个静态 xDS 上游 cluster。
			"clusters": []any{
				map[string]any{
					"name":                   "xds_cluster",
					"type":                   "STRICT_DNS",
					"connect_timeout":        "3s",
					"http2_protocol_options": map[string]any{},
					"load_assignment": map[string]any{
						"cluster_name": "xds_cluster",
						"endpoints": []any{
							map[string]any{
								"lb_endpoints": []any{
									map[string]any{
										"endpoint": map[string]any{
											"address": socketAddress(config.XDSTarget),
										},
									},
								},
							},
						},
					},
				},
			},
		},
		"admin": map[string]any{
			// admin 仅监听本机回环地址。
			"address": socketAddress(config.AdminAddress),
		},
	}
	// 编码为 YAML 文本。
	payload, err := yaml.Marshal(document)
	if err != nil {
		return err
	}
	// 写入最终文件。
	return os.WriteFile(path, payload, 0o644)
}

// socketAddress 把 host:port 地址转换成 Envoy YAML 所需的结构。
func socketAddress(address string) map[string]any {
	// 拆解地址，便于写成 socket_address 结构。
	host, port := splitAddress(address)
	// 返回 YAML 需要的嵌套 map。
	return map[string]any{
		"socket_address": map[string]any{
			"address":    host,
			"port_value": port,
		},
	}
}

// splitAddress 把 host:port 地址拆成字符串 host 与整数 port。
func splitAddress(address string) (string, int) {
	// 使用标准库拆解 host:port，避免手写解析误差。
	host, rawPort, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", 0
	}
	// 端口继续转换成整数，方便写入 YAML。
	port := 0
	fmt.Sscanf(rawPort, "%d", &port)
	// 返回拆分结果。
	return host, port
}
