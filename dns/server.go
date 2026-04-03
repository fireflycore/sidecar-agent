package dns

import (
	"context"
	"net"
	"strings"

	"github.com/miekg/dns"
)

const meshSuffix = "svc.cluster.local."

// Server 是 V3 sidecar-agent 的最小 DNS 服务实现。
//
// 当前阶段只做两件事：
// 1. 拦截 *.svc.cluster.local 并返回 127.0.0.1
// 2. 将其他域名转发给上游 DNS
type Server struct {
	upstreamDNS string
	udpServer   *dns.Server
	tcpServer   *dns.Server
}

// New 创建一个最小 DNS Server。
func New(upstreamDNS string) *Server {
	return &Server{
		upstreamDNS: strings.TrimSpace(upstreamDNS),
	}
}

// Start 启动 UDP/TCP 两个 DNS 监听。
//
// 这里同时监听 UDP 和 TCP，是为了兼容常见 DNS 查询场景。
func (s *Server) Start(addr string) error {
	s.udpServer = &dns.Server{
		Addr:    addr,
		Net:     "udp",
		Handler: s.handler(),
	}
	s.tcpServer = &dns.Server{
		Addr:    addr,
		Net:     "tcp",
		Handler: s.handler(),
	}

	udpErrCh := make(chan error, 1)
	go func() {
		udpErrCh <- s.udpServer.ListenAndServe()
	}()

	if err := s.tcpServer.ListenAndServe(); err != nil {
		_ = s.udpServer.ShutdownContext(context.Background())
		return err
	}

	if err := <-udpErrCh; err != nil {
		_ = s.tcpServer.ShutdownContext(context.Background())
		return err
	}

	return nil
}

// handler 返回当前 DNS Server 使用的请求分发器。
func (s *Server) handler() dns.Handler {
	mux := dns.NewServeMux()
	mux.HandleFunc(meshSuffix, s.handleMeshDomain)
	mux.HandleFunc(".", s.forward)
	return mux
}

// Shutdown 关闭当前 DNS Server。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.udpServer != nil {
		if err := s.udpServer.ShutdownContext(ctx); err != nil {
			return err
		}
	}
	if s.tcpServer != nil {
		if err := s.tcpServer.ShutdownContext(ctx); err != nil {
			return err
		}
	}
	return nil
}

// handleMeshDomain 对所有 mesh 内部域名统一返回 127.0.0.1。
func (s *Server) handleMeshDomain(w dns.ResponseWriter, r *dns.Msg) {
	resp := new(dns.Msg)
	resp.SetReply(r)
	resp.Authoritative = true

	for _, q := range r.Question {
		if q.Qtype != dns.TypeA {
			continue
		}
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{
				Name:   q.Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    0,
			},
			A: net.ParseIP("127.0.0.1"),
		})
	}

	_ = w.WriteMsg(resp)
}

// forward 将非 mesh 域名继续转发给上游 DNS。
func (s *Server) forward(w dns.ResponseWriter, r *dns.Msg) {
	client := new(dns.Client)
	resp, _, err := client.Exchange(r, s.upstreamDNS)
	if err != nil {
		dns.HandleFailed(w, r)
		return
	}
	_ = w.WriteMsg(resp)
}
