package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os/signal"
	"strconv"
	"syscall"

	sidecarconfig "github.com/fireflycore/sidecar-agent/config"
	meshdns "github.com/fireflycore/sidecar-agent/dns"
	"github.com/fireflycore/sidecar-agent/lb"
	"github.com/fireflycore/sidecar-agent/proxy"
	"github.com/fireflycore/sidecar-agent/registry"
)

// main 负责启动 V3 sidecar-agent 的最小骨架。
//
// 当前阶段会同时装配：
// 1. DNS Server
// 2. 代理入口
// 3. Consul registry
// 4. round-robin 选点器
func main() {
	configPath := flag.String("config", "", "sidecar-agent 配置文件路径")
	flag.Parse()

	cfg, err := sidecarconfig.Load(*configPath)
	if err != nil {
		log.Fatal(err)
	}

	dnsAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(cfg.DNSPort))
	dnsServer := meshdns.New(cfg.UpstreamDNS)

	consulRegistry, err := registry.New(cfg.ConsulAddr, cfg.ClusterName)
	if err != nil {
		log.Fatal(err)
	}
	proxyAddr := net.JoinHostPort("0.0.0.0", strconv.Itoa(cfg.AgentPort))
	proxyServer := proxy.New(consulRegistry, lb.NewRoundRobin())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		// 收到退出信号后，尽量优雅关闭 DNS Server。
		if shutdownErr := dnsServer.Shutdown(context.Background()); shutdownErr != nil {
			log.Printf("关闭 DNS Server 失败: %v", shutdownErr)
		}
		// 代理监听关闭后，Accept 循环会自然退出。
		if shutdownErr := proxyServer.Shutdown(); shutdownErr != nil {
			log.Printf("关闭 proxy 失败: %v", shutdownErr)
		}
	}()

	errCh := make(chan error, 2)

	go func() {
		log.Printf("sidecar-agent DNS server starting, addr=%s upstream=%s", dnsAddr, cfg.UpstreamDNS)
		errCh <- dnsServer.Start(dnsAddr)
	}()

	go func() {
		log.Printf("sidecar-agent proxy starting, addr=%s consul=%s", proxyAddr, cfg.ConsulAddr)
		errCh <- proxyServer.Start(proxyAddr)
	}()

	if err := <-errCh; err != nil {
		log.Fatal(err)
	}
}
