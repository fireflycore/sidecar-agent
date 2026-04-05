package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/fireflycore/sidecar-agent/app"
	"github.com/fireflycore/sidecar-agent/config"
)

// main 负责启动 sidecar-agent v2.1 当前阶段可运行骨架。
func main() {
	// 读取命令行中的配置文件路径。
	configPath := flag.String("config", "", "sidecar-agent 配置文件路径")
	flag.Parse()
	// 加载配置文件。
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	// 创建完整运行器。
	runner, err := app.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	// 创建信号上下文，统一接收退出信号。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// 启动所有模块。
	if err := runner.Start(ctx); err != nil {
		log.Fatal(err)
	}
	// 阻塞等待关闭。
	if err := runner.WaitForShutdown(ctx); err != nil {
		log.Fatal(err)
	}
}
