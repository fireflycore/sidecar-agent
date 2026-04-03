package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fireflycore/sidecar-agent/breaker"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Resolver 描述代理查询服务实例的最小依赖面。
type Resolver interface {
	Resolve(serviceName string) ([]string, error)
}

// Picker 描述代理从候选实例中选择一个地址的最小依赖面。
type Picker interface {
	Pick(instances []string) string
}

// Server 是 V3 sidecar-agent 的本地 gRPC 代理入口。
//
// 当前阶段它只支持最小 unary gRPC 转发链路：
// 1. 从 HTTP/2 请求的 authority 中解析服务名
// 2. 通过 registry 查询实例
// 3. 通过 lb 选点
// 4. 使用原始 protobuf bytes 调用真实下游服务
type Server struct {
	resolver    Resolver
	picker      Picker
	listener    net.Listener
	httpServer  *http.Server
	dialTimeout time.Duration
	mu          sync.Mutex
	conns       map[string]*grpc.ClientConn
	codec       rawProtoCodec
	breakers    map[string]*breaker.Breaker
}

// New 创建一个最小代理入口。
func New(resolver Resolver, picker Picker) *Server {
	return &Server{
		resolver:    resolver,
		picker:      picker,
		dialTimeout: 5 * time.Second,
		conns:       make(map[string]*grpc.ClientConn),
		codec:       rawProtoCodec{},
		breakers:    make(map[string]*breaker.Breaker),
	}
}

// Start 在指定地址启动代理监听。
func (s *Server) Start(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(listener)
}

// Serve 使用外部传入的 listener 启动代理。
func (s *Server) Serve(listener net.Listener) error {
	s.listener = listener
	s.httpServer = &http.Server{
		Handler: h2c.NewHandler(http.HandlerFunc(s.handleHTTP), &http2.Server{}),
	}
	err := s.httpServer.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown 关闭代理监听。
func (s *Server) Shutdown() error {
	if s.httpServer != nil {
		_ = s.httpServer.Shutdown(context.Background())
	}
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// parseServiceName 从 authority 中提取逻辑服务名。
//
// 例如：
// auth.default.svc.cluster.local:9090 -> auth
func parseServiceName(authority string) string {
	host := strings.TrimSpace(authority)
	if host == "" {
		return ""
	}
	if strings.Contains(host, ":") {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			host = parsedHost
		} else {
			host = strings.Split(host, ":")[0]
		}
	}

	parts := strings.Split(host, ".")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

// handleHTTP 作为当前阶段最小的 unary gRPC 代理处理器。
func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeGRPCError(w, status.Error(codes.Unimplemented, "only grpc POST is supported"))
		return
	}

	authority := r.Host
	serviceName := parseServiceName(authority)
	if serviceName == "" {
		writeGRPCError(w, status.Error(codes.InvalidArgument, "invalid authority"))
		return
	}

	serviceBreaker := s.breakerFor(serviceName)
	if err := serviceBreaker.Allow(); err != nil {
		writeGRPCError(w, status.Error(codes.Unavailable, err.Error()))
		return
	}

	instances, err := s.resolver.Resolve(serviceName)
	if err != nil {
		serviceBreaker.Record(false)
		writeGRPCError(w, err)
		return
	}

	target := s.picker.Pick(instances)
	if strings.TrimSpace(target) == "" {
		serviceBreaker.Record(false)
		writeGRPCError(w, status.Error(codes.Unavailable, "no upstream target available"))
		return
	}

	payload, err := decodeUnaryPayload(r.Body)
	if err != nil {
		serviceBreaker.Record(false)
		writeGRPCError(w, status.Error(codes.Internal, err.Error()))
		return
	}

	responsePayload, err := s.invokeUnary(r.Context(), target, authority, r.URL.Path, payload, incomingMetadata(r))
	if err != nil {
		serviceBreaker.Record(false)
		writeGRPCError(w, err)
		return
	}

	serviceBreaker.Record(true)
	writeGRPCResponse(w, responsePayload)
}

// handleHTTP 只处理最小 unary 调用，因此请求体必须是一个完整的 gRPC message frame。
func decodeUnaryPayload(body io.ReadCloser) ([]byte, error) {
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if len(data) < 5 {
		return nil, fmt.Errorf("grpc frame too short: %d", len(data))
	}
	if data[0] != 0 {
		return nil, fmt.Errorf("compressed grpc payload is not supported")
	}

	payloadLength := binary.BigEndian.Uint32(data[1:5])
	if len(data[5:]) != int(payloadLength) {
		return nil, fmt.Errorf("grpc payload length mismatch: want %d got %d", payloadLength, len(data[5:]))
	}
	return data[5:], nil
}

// writeGRPCResponse 按 gRPC wire format 写回 unary 响应。
func writeGRPCResponse(w http.ResponseWriter, payload []byte) {
	w.Header().Set("Content-Type", "application/grpc+proto")
	w.Header().Set("Trailer", "grpc-status, grpc-message")
	w.WriteHeader(http.StatusOK)

	frame := make([]byte, 5+len(payload))
	frame[0] = 0
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	_, _ = w.Write(frame)

	w.Header().Set("grpc-status", "0")
	w.Header().Set("grpc-message", "")
}

// writeGRPCError 按 gRPC trailer 语义把错误返回给客户端。
func writeGRPCError(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	if !ok {
		st = status.New(codes.Internal, err.Error())
	}

	w.Header().Set("Content-Type", "application/grpc+proto")
	w.Header().Set("Trailer", "grpc-status, grpc-message")
	w.WriteHeader(http.StatusOK)
	w.Header().Set("grpc-status", fmt.Sprintf("%d", st.Code()))
	w.Header().Set("grpc-message", st.Message())
}

// invokeUnary 使用原始 protobuf bytes 调下游真实 gRPC 服务。
func (s *Server) invokeUnary(ctx context.Context, target, authority, method string, payload []byte, md metadata.MD) ([]byte, error) {
	conn, err := s.conn(ctx, target, authority)
	if err != nil {
		return nil, err
	}

	request := &rawMessage{Payload: payload}
	response := &rawMessage{}
	callCtx := metadata.NewOutgoingContext(ctx, md)

	if err := conn.Invoke(callCtx, method, request, response, grpc.ForceCodec(s.codec)); err != nil {
		return nil, err
	}
	return response.Payload, nil
}

// conn 复用到同一个上游实例的 gRPC 连接。
func (s *Server) conn(ctx context.Context, target, authority string) (*grpc.ClientConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := target + "|" + authority
	if conn, ok := s.conns[key]; ok {
		return conn, nil
	}

	dialCtx, cancel := context.WithTimeout(ctx, s.dialTimeout)
	defer cancel()

	conn, err := grpc.DialContext(
		dialCtx,
		target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithAuthority(authority),
	)
	if err != nil {
		return nil, err
	}

	s.conns[key] = conn
	return conn, nil
}

// incomingMetadata 将 HTTP 请求头转换为下游 gRPC metadata。
func incomingMetadata(r *http.Request) metadata.MD {
	md := metadata.MD{}
	for key, values := range r.Header {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if lowerKey == "" {
			continue
		}
		if lowerKey == "content-type" || lowerKey == "te" || lowerKey == "user-agent" {
			continue
		}
		md[lowerKey] = append(md[lowerKey], values...)
	}
	return md
}

// breakerFor 按服务名维护独立熔断器，避免不同服务相互影响。
func (s *Server) breakerFor(serviceName string) *breaker.Breaker {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := strings.TrimSpace(serviceName)
	if b, ok := s.breakers[key]; ok {
		return b
	}

	// 当前阶段沿用 mesh-agent-plan 示例中的默认语义：
	// - 失败率阈值 0.5
	// - 最少请求数 10
	// - 冷却期 30 秒
	b := breaker.New(0.5, 10, 30*time.Second)
	s.breakers[key] = b
	return b
}

// rawMessage 让代理层可以直接透传已经编码好的 protobuf bytes。
type rawMessage struct {
	Payload []byte
}

// rawProtoCodec 让代理调用下游时不必依赖目标业务 proto 的具体 Go 类型。
type rawProtoCodec struct{}

func (c rawProtoCodec) Marshal(v any) ([]byte, error) {
	msg, ok := v.(*rawMessage)
	if !ok {
		return nil, fmt.Errorf("raw proto codec expects *rawMessage, got %T", v)
	}
	return msg.Payload, nil
}

func (c rawProtoCodec) Unmarshal(data []byte, v any) error {
	msg, ok := v.(*rawMessage)
	if !ok {
		return fmt.Errorf("raw proto codec expects *rawMessage, got %T", v)
	}
	msg.Payload = append(msg.Payload[:0], data...)
	return nil
}

func (c rawProtoCodec) Name() string {
	return "proto"
}
