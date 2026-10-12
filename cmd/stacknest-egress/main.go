package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"stacknest/server/internal/egress"
	"syscall"
)

// main 启动独立 Go 宿主服务，处理退出信号并保留已加载规则。
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := egress.Run(ctx, os.Args[1:]); err != nil {
		slog.Error("egress stopped", "error", err)
		os.Exit(1)
	}
}
