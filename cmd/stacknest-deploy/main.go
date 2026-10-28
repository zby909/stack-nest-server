package main

import (
	"context"
	"os"
	"os/signal"
	"stacknest/server/internal/deployclient"
	"syscall"
)

// main 在 Ctrl+C 时终止传输，服务端已确认进度仍可恢复。
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := deployclient.Main(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
