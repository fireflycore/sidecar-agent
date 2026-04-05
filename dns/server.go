package dns

import (
	"context"
	"net"
	"strings"

	miekgdns "github.com/miekg/dns"
)

const meshSuffix = "svc.cluster.local."

// Server 提供最小可用的本地 DNS 拦截能力。
type Server struct {
	// upstreamDNS 表示非 mesh 域名的上游 DNS。
	upstreamDNS string
	// udpServer 表示 UDP 监听实例。
	udpServer *miekgdns.Server
	// tcpServer 表示 TCP 监听实例。
	tcpServer *miekgdns.Server
}

// New 创建一个新的 DNS 服务器。
func New(upstreamDNS string) *Server {
	// 只保存裁剪后的上游地址，避免重复处理。
	return &Server{
		upstreamDNS: strings.TrimSpace(upstreamDNS),
	}
}

// Start 同时启动 UDP 与 TCP DNS 监听。
func (s *Server) Start(addr string) error {
	// 先构造共享处理器。
	handler := s.handler()
	// 初始化 UDP DNS 服务。
	s.udpServer = &miekgdns.Server{
		Addr:    addr,
		Net:     "udp",
		Handler: handler,
	}
	// 初始化 TCP DNS 服务。
	s.tcpServer = &miekgdns.Server{
		Addr:    addr,
		Net:     "tcp",
		Handler: handler,
	}
	// 单独启动 UDP 服务，避免阻塞当前流程。
	udpErrCh := make(chan error, 1)
	go func() {
		// 把 UDP 监听结果回传给主流程。
		udpErrCh <- s.udpServer.ListenAndServe()
	}()
	// TCP 监听放在当前 goroutine，便于直接接收退出结果。
	if err := s.tcpServer.ListenAndServe(); err != nil {
		// 如果 TCP 启动失败，主动关闭已启动的 UDP 服务。
		_ = s.udpServer.ShutdownContext(context.Background())
		// 返回启动错误。
		return err
	}
	// 兼容 UDP 服务异常退出场景。
	if err := <-udpErrCh; err != nil {
		// UDP 失败时同步关闭 TCP 服务。
		_ = s.tcpServer.ShutdownContext(context.Background())
		// 返回底层错误。
		return err
	}
	// 两个监听都正常退出时返回 nil。
	return nil
}

// Shutdown 关闭 DNS 监听。
func (s *Server) Shutdown(ctx context.Context) error {
	// 如果 UDP 服务已创建，则先执行关闭。
	if s.udpServer != nil {
		if err := s.udpServer.ShutdownContext(ctx); err != nil {
			return err
		}
	}
	// 如果 TCP 服务已创建，则再执行关闭。
	if s.tcpServer != nil {
		if err := s.tcpServer.ShutdownContext(ctx); err != nil {
			return err
		}
	}
	// 全部关闭成功后返回 nil。
	return nil
}

// handler 返回统一 DNS 请求分发器。
func (s *Server) handler() miekgdns.Handler {
	// 复用官方 ServeMux 简化域名匹配。
	mux := miekgdns.NewServeMux()
	// 所有 *.svc.cluster.local 请求都走本地回环地址。
	mux.HandleFunc(meshSuffix, s.handleMeshDomain)
	// 其他域名回退到上游 DNS。
	mux.HandleFunc(".", s.forward)
	// 返回组装后的处理器。
	return mux
}

// handleMeshDomain 对 mesh 内部域名固定返回 127.0.0.1。
func (s *Server) handleMeshDomain(w miekgdns.ResponseWriter, r *miekgdns.Msg) {
	// 创建响应消息并继承请求头。
	resp := new(miekgdns.Msg)
	resp.SetReply(r)
	// 标记为权威应答，便于本机调试时识别。
	resp.Authoritative = true
	// 遍历请求中的所有问题。
	for _, question := range r.Question {
		// 当前只处理 A 记录。
		if question.Qtype != miekgdns.TypeA {
			continue
		}
		// 把 svc 域名统一映射到 127.0.0.1。
		resp.Answer = append(resp.Answer, &miekgdns.A{
			Hdr: miekgdns.RR_Header{
				Name:   question.Name,
				Rrtype: miekgdns.TypeA,
				Class:  miekgdns.ClassINET,
				Ttl:    0,
			},
			A: net.ParseIP("127.0.0.1"),
		})
	}
	// 写出响应消息。
	_ = w.WriteMsg(resp)
}

// forward 把非 mesh 域名请求继续转发给上游解析器。
func (s *Server) forward(w miekgdns.ResponseWriter, r *miekgdns.Msg) {
	// 创建一个最小 DNS 客户端。
	client := new(miekgdns.Client)
	// 把原始请求透传给上游。
	resp, _, err := client.Exchange(r, s.upstreamDNS)
	if err != nil {
		// 上游失败时返回标准 DNS 失败响应。
		miekgdns.HandleFailed(w, r)
		return
	}
	// 把上游响应原样返回给调用方。
	_ = w.WriteMsg(resp)
}
