package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"runtime/debug"
	"syscall"

	"chenxi/internal/cx"
)

func main() {
	configPath := flag.String("config", "config.json", "配置文件路径")
	cli := flag.Bool("cli", false, "交互 CLI 模式（不启动 HTTP）")
	flag.Parse()

	cfg, err := cx.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("配置错误：%v", err)
	}

	// 1 GB 机器上最重要的两行：宁可主动 GC，也不要把盒子 OOM 掉。
	debug.SetGCPercent(cfg.Budgets.GOGC)
	debug.SetMemoryLimit(int64(cfg.Budgets.GoMemLimitMiB) << 20)
	log.Printf("晨曦启动：Go 堆软上限 %d MiB / GC %d%% / 并发上限 %d / 本地推理禁用",
		cfg.Budgets.GoMemLimitMiB, cfg.Budgets.GOGC, cfg.Budgets.MaxConcurrency)

	app, err := cx.New(cfg)
	if err != nil {
		log.Fatalf("初始化失败：%v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *cli {
		app.RunCLI(ctx)
		return
	}
	app.StartCron(ctx)
	log.Printf("HTTP 监听 %s", cfg.Listen)
	if err := app.Serve(ctx); err != nil {
		log.Printf("服务结束：%v", err)
	}
}
