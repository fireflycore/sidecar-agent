package registry

import (
	"context"
	"testing"
	"time"

	"github.com/fireflycore/sidecar-agent/model"
	"github.com/hashicorp/consul/api"
)

// fakeAgentAPI 提供最小的 agent 接口桩实现。
type fakeAgentAPI struct {
	// registered 保存最后一次注册请求。
	registeredID string
	// registeredService 保存最后一次完整注册对象。
	registeredService *api.AgentServiceRegistration
	// deregistered 保存最后一次注销实例 ID。
	deregistered string
	// deregisteredIDs 保存所有注销实例 ID，便于断言清理逻辑。
	deregisteredIDs []string
	// maintenance 保存最后一次维护模式实例 ID。
	maintenance string
	// updatedTTL 保存最后一次续租 check ID。
	updatedTTL string
}

// ServiceRegister 记录注册调用。
func (f *fakeAgentAPI) ServiceRegister(service *api.AgentServiceRegistration) error {
	// 记录注册实例 ID。
	f.registeredID = service.ID
	// 保存最后一次完整注册对象，便于断言 checks 与 meta。
	f.registeredService = service
	// 返回成功结果。
	return nil
}

// ServiceDeregister 记录注销调用。
func (f *fakeAgentAPI) ServiceDeregister(serviceID string) error {
	// 记录注销实例 ID。
	f.deregistered = serviceID
	// 同时把本次注销加入历史列表，便于断言旧轮次清理。
	f.deregisteredIDs = append(f.deregisteredIDs, serviceID)
	// 返回成功结果。
	return nil
}

// EnableServiceMaintenance 记录维护模式调用。
func (f *fakeAgentAPI) EnableServiceMaintenance(serviceID, _ string) error {
	// 记录维护模式实例 ID。
	f.maintenance = serviceID
	// 返回成功结果。
	return nil
}

// UpdateTTL 记录 lease 续租调用。
func (f *fakeAgentAPI) UpdateTTL(checkID, _ string, _ string) error {
	// 保存最后一次被续租的 check id。
	f.updatedTTL = checkID
	// 返回成功结果。
	return nil
}

// fakeHealthAPI 提供最小健康查询桩。
type fakeHealthAPI struct {
	// entries 按服务名返回当前 Consul 健康条目。
	entries map[string][]*api.ServiceEntry
}

// Service 返回空实例集合。
func (f *fakeHealthAPI) Service(service, tag string, passingOnly bool, q *api.QueryOptions) ([]*api.ServiceEntry, *api.QueryMeta, error) {
	// 当前服务若无预置条目，则返回空切片。
	if f.entries == nil {
		return nil, nil, nil
	}
	return f.entries[service], nil, nil
}

// fakeCatalogAPI 提供最小 catalog 桩。
type fakeCatalogAPI struct {
	// services 保存当前 catalog 中的服务索引。
	services map[string][]string
}

// Services 返回空服务集。
func (f *fakeCatalogAPI) Services(q *api.QueryOptions) (map[string][]string, *api.QueryMeta, error) {
	// 若未预置任何索引，则返回空 map。
	if f.services == nil {
		return map[string][]string{}, nil, nil
	}
	return f.services, nil, nil
}

// fakeKVAPI 提供最小 KV 桩。
type fakeKVAPI struct {
	// value 保存当前 key 的路由文档。
	value map[string][]byte
}

// Get 返回当前 key 的值。
func (f *fakeKVAPI) Get(key string, q *api.QueryOptions) (*api.KVPair, *api.QueryMeta, error) {
	// 若还没有初始化 map，则视为空。
	if f.value == nil {
		return nil, nil, nil
	}
	// 未命中时返回 nil。
	if _, ok := f.value[key]; !ok {
		return nil, nil, nil
	}
	// 返回命中的 KV 内容。
	return &api.KVPair{
		Key:   key,
		Value: f.value[key],
	}, nil, nil
}

// Put 写入当前 key 的值。
func (f *fakeKVAPI) Put(pair *api.KVPair, q *api.WriteOptions) (*api.WriteMeta, error) {
	// 惰性初始化内部 map。
	if f.value == nil {
		f.value = make(map[string][]byte)
	}
	// 保存写入值。
	f.value[pair.Key] = pair.Value
	// 返回成功结果。
	return nil, nil
}

// TestRegisterDrainAndDeregister 验证本地生命周期闭环。
func TestRegisterDrainAndDeregister(t *testing.T) {
	// 创建最小 fake 依赖。
	agent := &fakeAgentAPI{}
	kv := &fakeKVAPI{}
	// 创建 registry 客户端。
	client := NewWithClient(Settings{
		RouteKVPrefix:                  "routes",
		ClusterName:                    "cluster-a",
		Zone:                           "idc-a-1",
		HostIP:                         "127.0.0.1",
		Env:                            "prod",
		AgentLeaseTTL:                  10 * time.Second,
		AgentLeaseRefreshInterval:      3 * time.Second,
		DeregisterCriticalServiceAfter: 30 * time.Second,
	}, nil, nil, agent, &fakeHealthAPI{}, &fakeCatalogAPI{}, kv)
	// 构造一份合法注册请求。
	service, err := client.Register(context.Background(), model.RegisterRequest{
		AppID:     "10001",
		AppName:   "auth-center",
		Name:      "auth",
		Namespace: "default",
		Port:      9090,
		DNS:       "auth.default.svc.cluster.local",
		Env:       "prod",
		Weight:    100,
		Protocol:  "grpc",
		Kernel: model.KernelInfo{
			Language: "go",
			Version:  "go-micro/v1.12.0",
		},
		Methods: []string{
			"/acme.auth.v1.AuthService/Login",
		},
		Version: "v1.3.4",
	})
	// 注册必须成功。
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	// 注册后应进入 Serving。
	if got, want := service.State, model.StateServing; got != want {
		t.Fatalf("unexpected state after register: got=%s want=%s", got, want)
	}
	// 路由文档应已写入 KV。
	if _, ok := kv.value["routes/prod/default/auth/current"]; !ok {
		t.Fatal("expected route document to be written")
	}
	// 注册对象应带 ownership TTL 检查。
	if got, want := len(agent.registeredService.Checks), 2; got != want {
		t.Fatalf("unexpected check count: got=%d want=%d", got, want)
	}
	// 注册后应立即刷新一次 lease。
	if got, want := agent.updatedTTL, service.LeaseCheckID; got != want {
		t.Fatalf("unexpected lease check id: got=%s want=%s", got, want)
	}
	// 执行摘流。
	drained, err := client.Drain(model.DrainRequest{
		Name:           "auth",
		Port:           9090,
		GracePeriodRaw: "20s",
	})
	// 摘流必须成功。
	if err != nil {
		t.Fatalf("drain failed: %v", err)
	}
	// 摘流后状态应切到 Draining。
	if got, want := drained.State, model.StateDraining; got != want {
		t.Fatalf("unexpected state after drain: got=%s want=%s", got, want)
	}
	// agent 维护模式应命中同一个实例 ID。
	if got, want := agent.maintenance, service.InstanceID; got != want {
		t.Fatalf("unexpected maintenance instance: got=%s want=%s", got, want)
	}
	// 执行强制注销。
	deregistered, err := client.Deregister(model.DeregisterRequest{
		Name: "auth",
		Port: 9090,
	})
	// 注销必须成功。
	if err != nil {
		t.Fatalf("deregister failed: %v", err)
	}
	// 注销后状态应切到 Deregistered。
	if got, want := deregistered.State, model.StateDeregistered; got != want {
		t.Fatalf("unexpected state after deregister: got=%s want=%s", got, want)
	}
	// 注销动作应落到同一个实例 ID。
	if got, want := agent.deregistered, service.InstanceID; got != want {
		t.Fatalf("unexpected deregister instance: got=%s want=%s", got, want)
	}
}

// TestStartCleansPreviousRunServices 验证 agent 启动时会清理本机旧轮次残留注册。
func TestStartCleansPreviousRunServices(t *testing.T) {
	// 创建最小 fake 依赖，并预置一条旧轮次服务记录。
	agent := &fakeAgentAPI{}
	health := &fakeHealthAPI{
		entries: map[string][]*api.ServiceEntry{
			"auth": {
				{
					Service: &api.AgentService{
						ID:      "old-instance",
						Service: "auth",
						Address: "127.0.0.1",
						Port:    9090,
						Meta: map[string]string{
							"agent_id":     "cluster-a:idc-a-1:127.0.0.1",
							"agent_run_id": "old-run",
						},
					},
				},
				{
					Service: &api.AgentService{
						ID:      "other-instance",
						Service: "auth",
						Address: "127.0.0.2",
						Port:    9090,
						Meta: map[string]string{
							"agent_id":     "cluster-a:idc-a-1:127.0.0.2",
							"agent_run_id": "other-run",
						},
					},
				},
			},
		},
	}
	catalog := &fakeCatalogAPI{
		services: map[string][]string{
			"auth": nil,
		},
	}
	client := NewWithClient(Settings{
		RouteKVPrefix:                  "routes",
		ClusterName:                    "cluster-a",
		Zone:                           "idc-a-1",
		HostIP:                         "127.0.0.1",
		Env:                            "prod",
		AgentLeaseTTL:                  10 * time.Second,
		AgentLeaseRefreshInterval:      3 * time.Second,
		DeregisterCriticalServiceAfter: 30 * time.Second,
	}, nil, nil, agent, health, catalog, &fakeKVAPI{})
	// 启动 registry 后，应只清理属于当前 agent_id 的旧轮次服务。
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer func() {
		_ = client.Shutdown(context.Background())
	}()
	if got, want := len(agent.deregisteredIDs), 1; got != want {
		t.Fatalf("unexpected deregister count: got=%d want=%d", got, want)
	}
	if got, want := agent.deregisteredIDs[0], "old-instance"; got != want {
		t.Fatalf("unexpected cleaned instance: got=%s want=%s", got, want)
	}
}
