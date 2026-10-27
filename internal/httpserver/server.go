package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/doctor"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
	"stacknest/server/internal/resumable"
	"stacknest/server/internal/storage"
	"stacknest/server/internal/systeminfo"
)

// Config 定义 HTTP Server 所需配置。
type Config struct {
	Apps                *containerapp.Service
	Network             NetworkControl
	Addr                string
	WebDir              string
	SitesDir            string
	BaseDomain          string
	BackupStatusFile    string
	InvitationLoginURL  string
	MaxUploadSize       int64
	Storage             *storage.Guard
	Inspect             func(context.Context) doctor.Report
	CheckDatabase       func(context.Context) error
	ReadSystem          func(context.Context) (systeminfo.Usage, error)
	Activity            *activity.Store
	DisableAuthForTests bool
}

// ProjectService 是 HTTP 层实际需要的最小项目业务接口。
type ProjectService interface {
	ChangesAs(ctx context.Context, slug string, actor project.Actor, targetID, baseID string) ([]project.FileChange, error)
	PublishDraftAs(ctx context.Context, slug string, actor project.Actor, draftID, expectedID string) (project.CreatedProject, error)
	DiscardDraftAs(ctx context.Context, slug string, actor project.Actor, draftID string) (project.Project, error)
	OpenPreview(ctx context.Context, id string) (*os.Root, error)
	FilesAs(ctx context.Context, slug string, actor project.Actor, deploymentID string) ([]project.PublishedFile, error)
	FileUsageAs(ctx context.Context, expected project.Project, actor project.Actor) (map[string]project.PublishedFiles, error)
	SetAccessAs(ctx context.Context, slug string, actor project.Actor, paused bool, expectedAt time.Time) (project.Project, error)
	List(ctx context.Context) ([]project.Project, error)
	Get(ctx context.Context, slug string) (project.Project, error)
	Create(ctx context.Context, input project.CreateInput) (project.CreatedProject, error)
	DeleteAs(ctx context.Context, slug string, actor project.Actor) error
	UpdateAs(ctx context.Context, slug string, actor project.Actor, input project.UpdateInput) (project.CreatedProject, error)
	RollbackAs(ctx context.Context, slug string, actor project.Actor, expectedID string) (project.CreatedProject, error)
	EditAs(ctx context.Context, slug string, actor project.Actor, input project.MetadataInput) (project.Project, error)
	ExportAs(ctx context.Context, slug string, actor project.Actor, deploymentID string) (project.ExportedProject, error)
}

// Server 是 StackNest 的 HTTP 传输层。
type Server struct {
	uploads      *resumable.Store
	chunkMu      sync.Mutex
	config       Config
	projects     ProjectService
	auth         *auth.Service
	limiter      *loginLimiter
	http         *http.Server
	uploadMu     sync.Mutex
	exportMu     sync.Mutex
	diagnosticMu sync.Mutex
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
		uploads:  resumable.New(config.Storage, config.MaxUploadSize),
		config:   config,
		projects: projects,
		auth:     authService,
		limiter:  newLoginLimiter(),
	}

	mux := http.NewServeMux()
	generated.HandlerFromMux(server, mux)
	mux.HandleFunc("GET /api/version", server.GetVersion)
	mux.HandleFunc("POST /api/system/diagnostics", server.RunDiagnostics)
	mux.HandleFunc("PUT /api/users/{id}/role", server.ChangeUserRole)
	mux.HandleFunc("GET /api/apps/limits", server.GetAppLimits)
	mux.HandleFunc("PUT /api/apps/limits", server.UpdateAppLimits)
	mux.HandleFunc("GET /api/network", server.GetNetwork)
	mux.HandleFunc("PUT /api/network", server.UpdateNetwork)
	mux.HandleFunc("/sites/", server.serveSite)
	mux.HandleFunc("/", server.serveWeb)

	server.http = &http.Server{
		Addr:              config.Addr,
		Handler:           logging(server.appBoundary(server.previewBoundary(server.protect(mux)))),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return server
}

// Run 启动 HTTP 服务。
func (s *Server) Run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.cleanUploads(ctx)
	return s.http.ListenAndServe()
}

// cleanUploads 启动时和每小时回收过期续传，不依赖用户重新访问页面。
func (s *Server) cleanUploads(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := s.uploads.Cleanup(); err != nil {
			slog.Error("clean expired uploads", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Shutdown 优雅停止 HTTP 服务。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
