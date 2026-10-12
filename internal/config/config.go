package config

import (
	"os"
	"path/filepath"
	"stacknest/server/internal/storage"
	"strconv"
	"strings"
)

// Config 描述 StackNest 服务运行配置。
type Config struct {
	RunnerSocket         string
	EgressSocket         string
	AuthSuperAdminEmail  string
	Addr                 string
	DatabaseURL          string
	DataDir              string
	WebDir               string
	BaseDomain           string
	BackupStatusFile     string
	InvitationLoginURL   string
	MaxUploadSize        int64
	AuthAdminEmail       string
	AuthInitialUserEmail string
	AuthSecret           string
	SMTPHost             string
	SMTPPort             int
	SMTPUser             string
	SMTPPassword         string
}

// Load 从环境变量读取配置，并提供适合本地开发的默认值。
func Load() Config {
	smtpPort, err := strconv.Atoi(getenv("STACKNEST_SMTP_PORT", "465"))
	if err != nil {
		smtpPort = 0
	}
	loginURL := strings.TrimSpace(os.Getenv("STACKNEST_INVITATION_LOGIN_URL"))
	if loginURL == "" {
		if domain := strings.TrimSpace(os.Getenv("STACKNEST_ADMIN_DOMAIN")); domain != "" {
			loginURL = "https://" + domain + "/login"
		}
	}
	return Config{
		RunnerSocket:         strings.TrimSpace(os.Getenv("STACKNEST_RUNNER_SOCKET")),
		EgressSocket:         strings.TrimSpace(os.Getenv("STACKNEST_EGRESS_SOCKET")),
		AuthSuperAdminEmail:  getenv("STACKNEST_AUTH_SUPER_ADMIN_EMAIL", strings.TrimSpace(os.Getenv("STACKNEST_AUTH_ADMIN_EMAIL"))),
		Addr:                 getenv("STACKNEST_ADDR", "127.0.0.1:8080"),
		DatabaseURL:          getenv("STACKNEST_DATABASE_URL", "postgres://stacknest:stacknest@localhost:15432/stacknest?sslmode=disable"),
		DataDir:              getenv("STACKNEST_DATA_DIR", "./data"),
		WebDir:               getenv("STACKNEST_WEB_DIR", "../web/dist"),
		BaseDomain:           strings.TrimSpace(os.Getenv("STACKNEST_BASE_DOMAIN")),
		BackupStatusFile:     strings.TrimSpace(os.Getenv("STACKNEST_BACKUP_STATUS_FILE")),
		InvitationLoginURL:   loginURL,
		MaxUploadSize:        storage.MaxArchiveBytes,
		AuthAdminEmail:       strings.TrimSpace(os.Getenv("STACKNEST_AUTH_ADMIN_EMAIL")),
		AuthInitialUserEmail: strings.TrimSpace(os.Getenv("STACKNEST_AUTH_INITIAL_USER_EMAIL")),
		AuthSecret:           os.Getenv("STACKNEST_AUTH_SECRET"),
		SMTPHost:             getenv("STACKNEST_SMTP_HOST", "smtp.qq.com"),
		SMTPPort:             smtpPort,
		SMTPUser:             strings.TrimSpace(os.Getenv("STACKNEST_SMTP_USER")),
		SMTPPassword:         os.Getenv("STACKNEST_SMTP_PASSWORD"),
	}
}

// SitesDir 返回静态项目最终发布目录。
func (c Config) SitesDir() string {
	return filepath.Join(c.DataDir, "sites")
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
