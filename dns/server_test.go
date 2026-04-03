package dns

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

func TestHandleMeshDomainReturnsLoopback(t *testing.T) {
	// 这里直接测试内部域名处理逻辑，确保 *.svc.cluster.local 固定回到 127.0.0.1。
	server := New("127.0.0.1:1053")

	req := new(dns.Msg)
	req.SetQuestion("auth.default.svc.cluster.local.", dns.TypeA)

	recorder := new(dnsResponseRecorder)
	server.handleMeshDomain(recorder, req)

	if recorder.msg == nil {
		t.Fatal("expected dns response")
	}
	if len(recorder.msg.Answer) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(recorder.msg.Answer))
	}

	answer, ok := recorder.msg.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A record, got %T", recorder.msg.Answer[0])
	}
	if got := answer.A.String(); got != "127.0.0.1" {
		t.Fatalf("expected 127.0.0.1, got %s", got)
	}
}

func TestForwardUsesUpstreamDNS(t *testing.T) {
	// 这里起一个本地上游 DNS，验证非 mesh 域名会被继续转发。
	upstreamAddr, shutdown := startTestDNSServer(t)
	defer shutdown()

	server := New(upstreamAddr)
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)

	recorder := new(dnsResponseRecorder)
	server.forward(recorder, req)

	if recorder.msg == nil {
		t.Fatal("expected forwarded dns response")
	}
	if len(recorder.msg.Answer) != 1 {
		t.Fatalf("expected 1 answer, got %d", len(recorder.msg.Answer))
	}

	answer, ok := recorder.msg.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A record, got %T", recorder.msg.Answer[0])
	}
	if got := answer.A.String(); got != "10.0.0.10" {
		t.Fatalf("expected 10.0.0.10, got %s", got)
	}
}

func startTestDNSServer(t *testing.T) (string, func()) {
	t.Helper()

	// 使用随机端口避免和本地其他 DNS 服务冲突。
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen packet failed: %v", err)
	}

	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, r *dns.Msg) {
		resp := new(dns.Msg)
		resp.SetReply(r)
		resp.Answer = append(resp.Answer, &dns.A{
			Hdr: dns.RR_Header{
				Name:   r.Question[0].Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    0,
			},
			A: net.ParseIP("10.0.0.10"),
		})
		_ = w.WriteMsg(resp)
	})

	server := &dns.Server{
		PacketConn: packetConn,
		Handler:    mux,
	}
	go func() {
		_ = server.ActivateAndServe()
	}()

	return packetConn.LocalAddr().String(), func() {
		_ = server.Shutdown()
		_ = packetConn.Close()
	}
}

// dnsResponseRecorder 是测试用的最小响应记录器。
type dnsResponseRecorder struct {
	msg *dns.Msg
}

func (r *dnsResponseRecorder) LocalAddr() net.Addr {
	return &net.UDPAddr{}
}

func (r *dnsResponseRecorder) RemoteAddr() net.Addr {
	return &net.UDPAddr{}
}

func (r *dnsResponseRecorder) WriteMsg(msg *dns.Msg) error {
	r.msg = msg
	return nil
}

func (r *dnsResponseRecorder) Write([]byte) (int, error) {
	return 0, nil
}

func (r *dnsResponseRecorder) Close() error {
	return nil
}

func (r *dnsResponseRecorder) TsigStatus() error {
	return nil
}

func (r *dnsResponseRecorder) TsigTimersOnly(bool) {}

func (r *dnsResponseRecorder) Hijack() {}
