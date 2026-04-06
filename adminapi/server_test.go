package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fireflycore/sidecar-agent/model"
)

// fakeRegistry 提供管理接口测试需要的最小桩实现。
type fakeRegistry struct {
	// registered 保存最后一次 register 请求。
	registered model.RegisterRequest
	// services 保存当前本机服务状态。
	services []model.LocalService
}

// Register 记录注册请求并返回一个最小服务状态。
func (f *fakeRegistry) Register(ctx context.Context, request model.RegisterRequest) (model.LocalService, error) {
	// 保存请求，便于断言。
	f.registered = request
	// 构造一个最小返回值。
	service := model.LocalService{
		Request:    request,
		InstanceID: "instance-1",
		State:      model.StateServing,
	}
	// 同步更新本机服务列表。
	f.services = []model.LocalService{service}
	// 返回注册结果。
	return service, nil
}

// Drain 返回一个最小摘流结果。
func (f *fakeRegistry) Drain(request model.DrainRequest) (model.LocalService, error) {
	// 返回与请求对应的摘流状态。
	return model.LocalService{
		Request: model.RegisterRequest{
			Name: request.Name,
			Port: request.Port,
		},
		InstanceID: "instance-1",
		State:      model.StateDraining,
	}, nil
}

// Deregister 返回一个最小注销结果。
func (f *fakeRegistry) Deregister(request model.DeregisterRequest) (model.LocalService, error) {
	// 返回与请求对应的注销状态。
	return model.LocalService{
		Request: model.RegisterRequest{
			Name: request.Name,
			Port: request.Port,
		},
		InstanceID: "instance-1",
		State:      model.StateDeregistered,
	}, nil
}

// LocalServices 返回当前缓存的本机服务列表。
func (f *fakeRegistry) LocalServices() []model.LocalService {
	// 直接返回当前桩内状态。
	return f.services
}

// fakeRefresher 记录刷新调用次数。
type fakeRefresher struct {
	// count 记录被调用次数。
	count int
}

// RefreshNow 仅累计调用次数。
func (f *fakeRefresher) RefreshNow(ctx context.Context) error {
	// 每次调用都加一。
	f.count++
	// 返回成功结果。
	return nil
}

// TestRegisterHandler 验证 /register 会调用 registry 与 refresher。
func TestRegisterHandler(t *testing.T) {
	// 创建最小测试依赖。
	registry := &fakeRegistry{}
	refresher := &fakeRefresher{}
	server := New("127.0.0.1:0", registry, refresher, nil, nil, nil)
	// 构造一个合法 register 请求。
	body := `{"app_id":"10001","app_name":"auth-center","name":"auth","namespace":"default","port":9090,"dns":"auth.default.svc.cluster.local","env":"prod","weight":100,"protocol":"grpc","kernel":{"language":"go","version":"go-micro/v1.12.0"},"methods":["/acme.auth.v1.AuthService/Login"],"version":"v1.3.4"}`
	request := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body))
	writer := httptest.NewRecorder()
	// 直接调用处理函数。
	server.handleRegister(writer, request)
	// 响应码应为 200。
	if got, want := writer.Code, http.StatusOK; got != want {
		t.Fatalf("unexpected status code: got=%d want=%d", got, want)
	}
	// register 请求应成功传入 registry。
	if got, want := registry.registered.Name, "auth"; got != want {
		t.Fatalf("unexpected registered service: got=%s want=%s", got, want)
	}
	// 刷新动作应被触发一次。
	if got, want := refresher.count, 1; got != want {
		t.Fatalf("unexpected refresh count: got=%d want=%d", got, want)
	}
}

// TestDebugEndpointDisabled 验证关闭调试模式后不会暴露 /debug/services。
func TestDebugEndpointDisabled(t *testing.T) {
	// 创建未开启 debug 的服务实例。
	server := New("127.0.0.1:0", &fakeRegistry{}, nil, nil, nil, nil)
	// 使用底层 mux 发起一次 GET 请求。
	request := httptest.NewRequest(http.MethodGet, "/debug/services", nil)
	writer := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(writer, request)
	// 关闭 debug 后应返回 404。
	if got, want := writer.Code, http.StatusNotFound; got != want {
		t.Fatalf("unexpected status code: got=%d want=%d", got, want)
	}
}

// TestDebugServicesEndpoint 验证开启调试模式后可返回本机服务状态。
func TestDebugServicesEndpoint(t *testing.T) {
	// 先准备一份本机服务状态。
	registry := &fakeRegistry{
		services: []model.LocalService{
			{
				Request: model.RegisterRequest{
					Name: "auth",
					Port: 9090,
				},
				InstanceID: "instance-1",
				State:      model.StateServing,
			},
		},
	}
	server := New("127.0.0.1:0", registry, nil, nil, nil, nil)
	request := httptest.NewRequest(http.MethodGet, "/debug/services", nil)
	writer := httptest.NewRecorder()
	// 通过 mux 调用真实路由。
	server.httpServer.Handler.ServeHTTP(writer, request)
	// 接口应成功返回。
	if got, want := writer.Code, http.StatusOK; got != want {
		t.Fatalf("unexpected status code: got=%d want=%d", got, want)
	}
	// 解码响应体以校验服务内容。
	var services []model.LocalService
	if err := json.Unmarshal(writer.Body.Bytes(), &services); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if got, want := len(services), 1; got != want {
		t.Fatalf("unexpected service count: got=%d want=%d", got, want)
	}
}
