package httpserver

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
	"stacknest/server/internal/systeminfo"
)

// Config 定义 HTTP Server 所需配置。
type Config struct {
	Addr                string
	WebDir              string
	SitesDir            string
	BaseDomain          string
	MaxUploadSize       int64
	Storage             *storage.Guard
	CheckDatabase       func(context.Context) error
	ReadSystem          func(context.Context) (systeminfo.Usage, error)
	Activity            *activity.Store
	DisableAuthForTests bool
}

// ProjectService 是 HTTP 层实际需要的最小项目业务接口。
type ProjectService interface {
	List(ctx context.Context) ([]project.Project, error)
	Get(ctx context.Context, slug string) (project.Project, error)
	Create(ctx context.Context, input project.CreateInput) (project.CreatedProject, error)
	DeleteAs(ctx context.Context, slug string, actor project.Actor) error
	UpdateAs(ctx context.Context, slug string, actor project.Actor, input project.UpdateInput) (project.CreatedProject, error)
	RollbackAs(ctx context.Context, slug string, actor project.Actor, expectedID string) (project.CreatedProject, error)
}

// Server 是 StackNest 的 HTTP 传输层。
type Server struct {
	config   Config
	projects ProjectService
	auth     *auth.Service
	limiter  *loginLimiter
	http     *http.Server
	uploadMu sync.Mutex
}

var _ generated.ServerInterface = (*Server)(nil)

// New 创建 HTTP Server。
func New(config Config, projects ProjectService, authService *auth.Service) *Server {
	if authService == nil && !config.DisableAuthForTests {
		panic("StackNest authentication must be configured")
	}
	if config.Storage == nil {
		config.Storage = storage.New(filepath.Dir(config.SitesDir))
	}
	if config.ReadSystem == nil {
		config.ReadSystem = systeminfo.Read
	}
	server := &Server{
		config:   config,
		projects: projects,
		auth:     authService,
		limiter:  newLoginLimiter(),
	}

	mux := http.NewServeMux()
	generated.HandlerFromMux(server, mux)
	mux.HandleFunc("/sites/", server.serveSite)
	mux.HandleFunc("/", server.serveWeb)

	server.http = &http.Server{
		Addr:              config.Addr,
		Handler:           logging(server.previewBoundary(server.protect(mux))),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return server
}

// Run 启动 HTTP 服务。
func (s *Server) Run() error {
	return s.http.ListenAndServe()
}

// Shutdown 优雅停止 HTTP 服务。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
