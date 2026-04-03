package registry

import (
	"errors"
	"testing"

	"github.com/hashicorp/consul/api"
)

type fakeHealthService struct {
	entries []*api.ServiceEntry
	err     error
}

func (f *fakeHealthService) Service(service, tag string, passingOnly bool, q *api.QueryOptions) ([]*api.ServiceEntry, *api.QueryMeta, error) {
	return f.entries, nil, f.err
}

type fakeAgentService struct {
	registration *api.AgentServiceRegistration
	err          error
}

func (f *fakeAgentService) ServiceRegister(service *api.AgentServiceRegistration) error {
	f.registration = service
	return f.err
}

func TestConsulRegistryResolvePrefersLocalCluster(t *testing.T) {
	// 当本集群和远端集群都有实例时，必须优先只返回本集群实例。
	registry := &ConsulRegistry{
		health: &fakeHealthService{
			entries: []*api.ServiceEntry{
				newServiceEntry("10.0.0.1", 8080, []string{"cluster=cluster-a"}),
				newServiceEntry("10.0.0.2", 8080, []string{"cluster=cluster-b"}),
			},
		},
		clusterName: "cluster-a",
	}

	instances, err := registry.Resolve("auth")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(instances) != 1 || instances[0] != "10.0.0.1:8080" {
		t.Fatalf("expected local instance only, got %#v", instances)
	}
}

func TestConsulRegistryResolveFallsBackToRemote(t *testing.T) {
	// 当本集群没有实例时，允许溢出到其他集群。
	registry := &ConsulRegistry{
		health: &fakeHealthService{
			entries: []*api.ServiceEntry{
				newServiceEntry("10.0.1.2", 9090, []string{"cluster=cluster-b"}),
			},
		},
		clusterName: "cluster-a",
	}

	instances, err := registry.Resolve("auth")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(instances) != 1 || instances[0] != "10.0.1.2:9090" {
		t.Fatalf("expected remote fallback instance, got %#v", instances)
	}
}

func TestConsulRegistryRegister(t *testing.T) {
	// 注册时必须带上 cluster 标签和 gRPC 健康检查配置。
	agent := &fakeAgentService{}
	registry := &ConsulRegistry{
		agent:       agent,
		clusterName: "cluster-a",
	}

	if err := registry.Register("auth-1", "auth", "127.0.0.1", 8080); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if agent.registration == nil {
		t.Fatal("expected registration to be captured")
	}
	if len(agent.registration.Tags) != 1 || agent.registration.Tags[0] != "cluster=cluster-a" {
		t.Fatalf("unexpected tags: %#v", agent.registration.Tags)
	}
	if got := agent.registration.Check.GRPC; got != "127.0.0.1:8080" {
		t.Fatalf("unexpected grpc check: %s", got)
	}
}

func TestConsulRegistryResolveNoInstances(t *testing.T) {
	// 没有健康实例时要给出明确错误，便于上层决定如何处理。
	registry := &ConsulRegistry{
		health:      &fakeHealthService{},
		clusterName: "cluster-a",
	}

	_, err := registry.Resolve("auth")
	if !errors.Is(err, ErrNoInstances) {
		t.Fatalf("expected ErrNoInstances, got %v", err)
	}
}

func newServiceEntry(address string, port int, tags []string) *api.ServiceEntry {
	return &api.ServiceEntry{
		Node: &api.Node{
			Address: address,
		},
		Service: &api.AgentService{
			Address: address,
			Port:    port,
			Tags:    tags,
		},
	}
}
