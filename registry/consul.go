package registry

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/consul/api"
)

var ErrNoInstances = errors.New("no healthy instances available")

// healthService 抽象 Consul 健康查询接口，便于单元测试替换。
type healthService interface {
	Service(service, tag string, passingOnly bool, q *api.QueryOptions) ([]*api.ServiceEntry, *api.QueryMeta, error)
}

// agentService 抽象 Consul agent 注册接口，便于单元测试替换。
type agentService interface {
	ServiceRegister(service *api.AgentServiceRegistration) error
}

// ConsulRegistry 是 V3 Step 3 使用的 Consul 注册中心适配层。
//
// 它当前负责两件事：
// 1. 查询服务健康实例，并按 cluster 标签优先返回本集群实例
// 2. 代替业务服务向 Consul 执行服务注册
type ConsulRegistry struct {
	health      healthService
	agent       agentService
	clusterName string
}

// New 基于 Consul 地址和集群名创建注册中心客户端。
func New(addr, clusterName string) (*ConsulRegistry, error) {
	cfg := api.DefaultConfig()
	cfg.Address = strings.TrimSpace(addr)

	client, err := api.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	return &ConsulRegistry{
		health:      client.Health(),
		agent:       client.Agent(),
		clusterName: strings.TrimSpace(clusterName),
	}, nil
}

// Resolve 查询服务实例，本集群实例优先。
func (r *ConsulRegistry) Resolve(serviceName string) ([]string, error) {
	entries, _, err := r.health.Service(strings.TrimSpace(serviceName), "", true, nil)
	if err != nil {
		return nil, err
	}

	var local []string
	var remote []string

	for _, entry := range entries {
		address, ok := serviceAddress(entry)
		if !ok {
			continue
		}
		if hasClusterTag(entry, r.clusterName) {
			local = append(local, address)
			continue
		}
		remote = append(remote, address)
	}

	if len(local) > 0 {
		return local, nil
	}
	if len(remote) > 0 {
		return remote, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNoInstances, serviceName)
}

// Register 由 sidecar-agent 代替业务服务执行 Consul 注册。
func (r *ConsulRegistry) Register(id, name, address string, port int) error {
	return r.agent.ServiceRegister(&api.AgentServiceRegistration{
		ID:      strings.TrimSpace(id),
		Name:    strings.TrimSpace(name),
		Address: strings.TrimSpace(address),
		Port:    port,
		Tags:    []string{"cluster=" + r.clusterName},
		Check: &api.AgentServiceCheck{
			// 当前按规划使用 gRPC 健康检查地址。
			GRPC:                           fmt.Sprintf("%s:%d", strings.TrimSpace(address), port),
			Interval:                       "10s",
			DeregisterCriticalServiceAfter: "30s",
		},
	})
}

// serviceAddress 从 Consul 返回值中提取一个可访问的 host:port。
func serviceAddress(entry *api.ServiceEntry) (string, bool) {
	if entry == nil || entry.Service == nil {
		return "", false
	}

	address := strings.TrimSpace(entry.Service.Address)
	if address == "" && entry.Node != nil {
		// 某些服务可能只注册了 Node.Address，这里做一次回退。
		address = strings.TrimSpace(entry.Node.Address)
	}
	if address == "" || entry.Service.Port == 0 {
		return "", false
	}
	return fmt.Sprintf("%s:%d", address, entry.Service.Port), true
}

// hasClusterTag 判断实例是否属于当前集群。
func hasClusterTag(entry *api.ServiceEntry, clusterName string) bool {
	if entry == nil || entry.Service == nil {
		return false
	}

	expected := "cluster=" + strings.TrimSpace(clusterName)
	for _, tag := range entry.Service.Tags {
		if strings.TrimSpace(tag) == expected {
			return true
		}
	}
	return false
}
