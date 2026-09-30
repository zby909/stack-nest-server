package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/config"
	"stacknest/server/internal/httpserver"
	"stacknest/server/internal/postgres"
	"stacknest/server/internal/project"
	"stacknest/server/internal/staticdeploy"
	"stacknest/server/internal/storage"
)

// main 配置结构化日志并启动服务。
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("stacknest stopped", "error", err)
		os.Exit(1)
	}
}

// run 初始化持久化、恢复流程及 HTTP 服务。
func run() error {
	cfg := config.Load()

	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 10*time.Second)
	store, err := postgres.Open(connectCtx, cfg.DatabaseURL)
	cancelConnect()
	if err != nil {
		return err
	}
	defer store.Close()

	guard := storage.New(cfg.DataDir)
	activityLog, err := activity.New(cfg.DataDir)
	if err != nil {
		return err
	}
	deployer := staticdeploy.New(staticdeploy.Config{
		DataDir:  cfg.DataDir,
		MaxFiles: storage.MaxFiles,
		MaxBytes: storage.MaxExpandedBytes,
		Guard:    guard,
	})
	recoverCtx, cancelRecover := context.WithTimeout(context.Background(), 30*time.Second)
	err = deployer.RecoverRevisions(recoverCtx, store.Get)
	if err == nil {
		err = deployer.RecoverDeletions(recoverCtx, store.Get)
	}
	if err == nil {
		err = deployer.RecoverPublications(recoverCtx, store.Get)
	}
	cancelRecover()
	if err != nil {
		return err
	}
	projects := project.NewService(store, deployer)
	mailer, err := auth.NewSMTPMailer(auth.SMTPConfig{
		Host:     cfg.SMTPHost,
		Port:     cfg.SMTPPort,
		Username: cfg.SMTPUser,
		Password: cfg.SMTPPassword,
	})
	if err != nil {
		return err
	}
	authService, err := auth.NewService(store, mailer, cfg.AuthSecret)
	if err != nil {
		return err
	}
	if err := authService.BootstrapUsers(context.Background(), cfg.AuthAdminEmail, cfg.AuthInitialUserEmail); err != nil {
		return err
	}

	server := httpserver.New(httpserver.Config{
		Addr:          cfg.Addr,
		WebDir:        cfg.WebDir,
		SitesDir:      cfg.SitesDir(),
		BaseDomain:    cfg.BaseDomain,
		MaxUploadSize: cfg.MaxUploadSize,
		Storage:       guard,
		CheckDatabase: store.Ping,
		Activity:      activityLog,
	}, projects, authService)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("StackNest server started", "addr", cfg.Addr)
		errCh <- server.Run()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
