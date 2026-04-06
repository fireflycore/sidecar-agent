package adminapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/fireflycore/sidecar-agent/model"
	"github.com/fireflycore/sidecar-agent/xds"
)

// Registry 抽象管理接口依赖的本地注册中心能力。
type Registry interface {
	// Register 负责处理本地服务注册请求。
	Register(ctx context.Context, request model.RegisterRequest) (model.LocalService, error)
	// Drain 负责把本机实例切换到摘流状态。
	Drain(request model.DrainRequest) (model.LocalService, error)
	// Deregister 负责强制注销本机实例。
	Deregister(request model.DeregisterRequest) (model.LocalService, error)
	// LocalServices 返回当前宿主机所有已知服务状态。
	LocalServices() []model.LocalService
}

// Refresher 抽象 register/drain/deregister 后的主动收敛动作。
type Refresher interface {
	// RefreshNow 立即触发一次发现与 xDS 重建。
	RefreshNow(ctx context.Context) error
}

// Server 提供 v2.1 文档要求的本地管理接口。
type Server struct {
	// registry 保存生命周期状态与注册逻辑。
	registry Registry
	// refresher 用于在本地状态变化后主动收敛发现结果。
	refresher Refresher
	// xdsServer 用于输出调试摘要。
	xdsServer *xds.Server
	// metricsHandler 用于暴露 Prometheus 文本指标。
	metricsHandler http.Handler
	// logger 负责输出结构化日志。
	logger *slog.Logger
	// enableDebug 控制是否暴露调试接口。
	enableDebug bool
	// httpServer 保存底层 HTTP 服务。
	httpServer *http.Server
}

// New 创建一个新的管理接口服务。
func New(listenAddress string, enableDebug bool, registry Registry, refresher Refresher, xdsServer *xds.Server, metricsHandler http.Handler, logger *slog.Logger) *Server {
	// 先创建路由表。
	mux := http.NewServeMux()
	// 组装管理接口服务对象。
	server := &Server{
		registry:       registry,
		refresher:      refresher,
		xdsServer:      xdsServer,
		metricsHandler: metricsHandler,
		logger:         logger,
		enableDebug:    enableDebug,
		httpServer: &http.Server{
			Addr:              listenAddress,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
	// 注册文档要求的管理接口。
	mux.HandleFunc("POST /register", server.handleRegister)
	mux.HandleFunc("POST /drain", server.handleDrain)
	mux.HandleFunc("POST /deregister", server.handleDeregister)
	mux.HandleFunc("GET /healthz", server.handleHealthz)
	// 仅在调试开关打开时暴露调试接口。
	if enableDebug {
		mux.HandleFunc("GET /debug/services", server.handleDebugServices)
		mux.HandleFunc("GET /debug/xds", server.handleDebugXDS)
	}
	// 指标接口由 telemetry 配置决定是否挂载。
	if metricsHandler != nil {
		mux.Handle("GET /metrics", metricsHandler)
	}
	// 返回构造完成的服务对象。
	return server
}

// Start 启动 HTTP 管理接口。
func (s *Server) Start() error {
	// 输出启动日志。
	if s.logger != nil {
		s.logger.Info("admin api started",
			slog.String("address", s.httpServer.Addr),
		)
	}
	// 启动 HTTP 服务。
	err := s.httpServer.ListenAndServe()
	// 标准关闭错误不视为失败。
	if err == http.ErrServerClosed {
		return nil
	}
	// 其他错误直接返回。
	return err
}

// Shutdown 优雅关闭 HTTP 管理接口。
func (s *Server) Shutdown(ctx context.Context) error {
	// 调用标准库关闭流程。
	return s.httpServer.Shutdown(ctx)
}

// RefreshNow 适配 Refresher 接口为空时的行为。
func (s *Server) RefreshNow(ctx context.Context) error {
	// 未装配 refresher 时不执行任何动作。
	if s.refresher == nil {
		return nil
	}
	// 透传到真实刷新器。
	return s.refresher.RefreshNow(ctx)
}

// handleRegister 处理本地服务启动登记。
func (s *Server) handleRegister(writer http.ResponseWriter, request *http.Request) {
	// 解码 JSON 请求体。
	var payload model.RegisterRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	// 调用 registry 执行注册。
	service, err := s.registry.Register(request.Context(), payload)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	// 注册成功后立即触发一次快照收敛。
	if err := s.RefreshNow(request.Context()); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	// 返回最终服务状态。
	writeJSON(writer, http.StatusOK, service)
}

// handleDrain 处理优雅摘流请求。
func (s *Server) handleDrain(writer http.ResponseWriter, request *http.Request) {
	// 解码 JSON 请求体。
	var payload model.DrainRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	// 执行摘流。
	service, err := s.registry.Drain(payload)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	// 摘流后主动收敛发现与 xDS。
	if err := s.RefreshNow(request.Context()); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	// 返回最新服务状态。
	writeJSON(writer, http.StatusOK, service)
}

// handleDeregister 处理强制注销请求。
func (s *Server) handleDeregister(writer http.ResponseWriter, request *http.Request) {
	// 解码 JSON 请求体。
	var payload model.DeregisterRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	// 执行注销。
	service, err := s.registry.Deregister(payload)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	// 注销后立即刷新下游快照。
	if err := s.RefreshNow(request.Context()); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	// 返回最新状态。
	writeJSON(writer, http.StatusOK, service)
}

// handleHealthz 返回一个最小健康检查响应。
func (s *Server) handleHealthz(writer http.ResponseWriter, _ *http.Request) {
	// 返回固定的健康状态。
	writeJSON(writer, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// handleDebugServices 返回当前宿主机已知服务状态。
func (s *Server) handleDebugServices(writer http.ResponseWriter, _ *http.Request) {
	// 直接输出 registry 当前缓存的本机服务。
	writeJSON(writer, http.StatusOK, s.registry.LocalServices())
}

// handleDebugXDS 返回当前已发布快照摘要。
func (s *Server) handleDebugXDS(writer http.ResponseWriter, _ *http.Request) {
	// xDS 未装配时返回空摘要。
	if s.xdsServer == nil {
		writeJSON(writer, http.StatusOK, map[string]any{})
		return
	}
	// 返回当前 xDS 摘要。
	writeJSON(writer, http.StatusOK, s.xdsServer.Summary())
}

// writeJSON 把对象写成 JSON 响应。
func writeJSON(writer http.ResponseWriter, statusCode int, payload any) {
	// 先设置响应头和状态码。
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)
	// 忽略编码错误，避免在响应已开始后再次写状态码。
	_ = json.NewEncoder(writer).Encode(payload)
}

// writeError 返回统一错误格式。
func writeError(writer http.ResponseWriter, statusCode int, err error) {
	// 错误响应同样使用 JSON 结构。
	writeJSON(writer, statusCode, map[string]string{
		"error": err.Error(),
	})
}
