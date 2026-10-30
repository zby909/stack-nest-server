package containerapp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrInvalid        = errors.New("应用输入无效")
	ErrNotFound       = errors.New("应用不存在或无权访问")
	ErrBusy           = errors.New("应用正在执行操作，请稍后重试")
	ErrQuota          = errors.New("应用数量或内存总配额不足")
	ErrConflict       = errors.New("资源配置已被更新，请刷新后重试")
	ErrUnavailable    = errors.New("Docker 运行器暂时不可用")
	ErrDeployCanceled = errors.New("已取消本次部署")
	ErrCancelTooLate  = errors.New("部署已进入容器切换阶段，无法取消，请等待完成")
)

const MaxApps = 3
const MaxMemoryMiB = 1024
const TotalMemoryMiB = 768
const MaxImageBytes int64 = 2 << 30
const HostPrefix = "stack-nest--app-"

// ConfigJSONLimit 为合法字段的六倍 JSON 转义留足空间，后台和运行器保持同一上限。
const ConfigJSONLimit = 256 << 10

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var imagePattern = regexp.MustCompile(`^(?:[a-z0-9][a-z0-9.-]*(?::[0-9]+)?/)?[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|@sha256:[a-f0-9]{64})?$`)

// Registry 保存私有仓库凭据，仅在加密配置与运行器内部传递。
type Registry struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Spec 是受平台约束的单个 HTTP 容器配置，不接受任意 Docker 参数。
type Spec struct {
	Image      string            `json:"image"`
	Port       int               `json:"port"`
	MemoryMiB  int               `json:"memoryMiB"`
	CPUs       float64           `json:"cpus"`
	DataPath   string            `json:"dataPath"`
	HealthPath string            `json:"healthPath"`
	Env        map[string]string `json:"env"`
	Registry   *Registry         `json:"registry,omitempty"`
}

// Input 允许保留已有敏感配置，编辑时不需要回显密码。
type Input struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
	Spec
	ReplaceEnv    bool               `json:"replaceEnv"`
	EnvPatch      map[string]*string `json:"envPatch,omitempty"`
	ClearRegistry bool               `json:"clearRegistry"`
}

// App 在数据库保存配置密文，公开接口仅返回非敏感配置。
type App struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"ownerId"`
	OwnerEmail   string    `json:"ownerEmail"`
	Name         string    `json:"name"`
	Slug         string    `json:"slug"`
	Image        string    `json:"image"`
	Port         int       `json:"port"`
	MemoryMiB    int       `json:"memoryMiB"`
	CPUs         float64   `json:"cpus"`
	DataPath     string    `json:"dataPath"`
	HealthPath   string    `json:"healthPath"`
	EnvKeys      []string  `json:"envKeys"`
	HasRegistry  bool      `json:"hasRegistry"`
	Status       string    `json:"status"`
	Action       string    `json:"action,omitempty"`
	Error        string    `json:"error,omitempty"`
	Canceled     bool      `json:"canceled,omitempty"`
	Generation   int64     `json:"generation"`
	ContainerID  string    `json:"-"`
	ImageID      string    `json:"imageId,omitempty"`
	Sealed       string    `json:"-"`
	ActiveSealed string    `json:"-"`
	ActorID      string    `json:"-"`
	ActorEmail   string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	URL          string    `json:"url"`
}

// Actor 保留请求身份，后台工作完成时仍能记录操作人。
type Actor struct {
	ID, Email string
	Admin     bool
}

// Result 是运行器确认的容器状态，不含运行环境变量。
type Result struct {
	ContainerID string     `json:"containerId"`
	ImageID     string     `json:"imageId"`
	State       string     `json:"state"`
	OOMKilled   bool       `json:"oomKilled"`
	ExitCode    int        `json:"exitCode"`
	Health      string     `json:"health,omitempty"`
	HealthCheck string     `json:"healthCheck,omitempty"`
	Usage       *Usage     `json:"usage,omitempty"`
	Disk        *DiskUsage `json:"disk,omitempty"`
}

// Usage 是容器的当前采样，CPU 的 100% 表示占用一个核心，不包含宿主信息。
type Usage struct {
	CPUPercent       *float64  `json:"cpuPercent,omitempty"`
	MemoryBytes      uint64    `json:"memoryBytes"`
	MemoryLimitBytes uint64    `json:"memoryLimitBytes"`
	PIDs             uint64    `json:"pids"`
	SampledAt        time.Time `json:"sampledAt"`
}

// Operation 只表达平台允许的生命周期操作。
type Operation struct {
	Limits     *Limits `json:"limits,omitempty"`
	ID         string  `json:"id"`
	Generation int64   `json:"generation"`
	Action     string  `json:"action"`
	ImageID    string  `json:"imageId,omitempty"`
	Spec       Spec    `json:"spec"`
}

// Runtime 将 Docker 权限收敛在独立运行器的几个固定操作中。
type Runtime interface {
	Health(context.Context) error
	Capacity(context.Context) (Capacity, error)
	Progress(context.Context, string, int64) (Progress, error)
	Operate(context.Context, Operation) (Result, error)
	Cancel(context.Context, string, int64) error
	State(context.Context, string, string) (Result, error)
	Logs(context.Context, string, string) (string, error)
	Proxy(http.ResponseWriter, *http.Request, string, string)
}

// Store 定义队列与所有权的持久化边界。
type Store interface {
	AppLimits(context.Context) (LimitsView, error)
	UpdateAppLimits(context.Context, LimitsUpdate) (LimitsView, error)
	Apps(context.Context, Actor) ([]App, error)
	App(context.Context, string, Actor) (App, error)
	SaveApp(context.Context, App, Actor, bool) error
	QueueApp(context.Context, string, Actor, string, int64) (App, error)
	RecoverApps(context.Context) error
	ClaimApp(context.Context) (App, error)
	CompleteApp(context.Context, App, Result, string) error
	CancelPendingApp(context.Context, App) error
}

// ValidID 拒绝非规范 UUID，避免路径与 Docker 名称注入。
func ValidID(id string) bool { value, err := uuid.Parse(id); return err == nil && value.String() == id }

// ValidSlug 限制域名标签长度与字符。
func ValidSlug(slug string) bool { return slugPattern.MatchString(slug) }

// Validate 同时用于后台和运行器，防止内部调用绕过资源、路径限制。
func (s Spec) Validate() error {
	if len(s.Image) > 256 || !imagePattern.MatchString(s.Image) || strings.HasPrefix(s.Image, "sha256:") || strings.Contains(s.Image, "localhost") {
		return invalid("镜像地址格式无效，请使用仓库镜像地址")
	}
	if s.Port < 1 || s.Port > 65535 {
		return invalid("容器端口必须在 1–65535 之间")
	}
	if err := ValidateHealthPath(s.HealthPath); err != nil {
		return err
	}
	if s.MemoryMiB < 64 || s.MemoryMiB > MaxMemoryMiB || s.CPUs < 0.1 || s.CPUs > 1 || math.IsNaN(s.CPUs) || math.IsInf(s.CPUs, 0) {
		return invalid(fmt.Sprintf("内存须为 64–%d MiB，CPU 须为 0.1–1 核", MaxMemoryMiB))
	}
	if s.DataPath != "" {
		if len(s.DataPath) > 4096 || !strings.HasPrefix(s.DataPath, "/") || path.Clean(s.DataPath) != s.DataPath || strings.ContainsAny(s.DataPath, "\\\x00:") {
			return invalid("数据目录须为容器内的绝对路径")
		}
		for _, reserved := range []string{"/", "/proc", "/sys", "/dev", "/etc", "/bin", "/sbin", "/usr", "/run", "/var/run"} {
			if s.DataPath == reserved || reserved != "/" && strings.HasPrefix(s.DataPath, reserved+"/") {
				return invalid("不能将系统目录设为持久化目录")
			}
		}
	}
	if len(s.Env) > 64 {
		return invalid("环境变量最多 64 个")
	}
	size := 0
	for key, value := range s.Env {
		size += len(key) + len(value)
		if !envPattern.MatchString(key) || len(key) > 128 || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return invalid("环境变量名称或值无效")
		}
	}
	if size > 16384 {
		return invalid("环境变量总长度不能超过 16 KiB")
	}
	if s.Registry != nil && (s.Registry.Username == "" || s.Registry.Password == "" || len(s.Registry.Username) > 256 || len(s.Registry.Password) > 4096) {
		return invalid("私有仓库需要用户名和密码 / 访问令牌")
	}
	return nil
}

// ValidateHealthPath 只允许容器内的绝对路径，禁止指定其他地址、查询参数或片段。
func ValidateHealthPath(value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || len(value) > 256 || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") ||
		strings.ContainsAny(value, "\\\x00\r\n ?#") || parsed.Host != "" || parsed.RawQuery != "" ||
		parsed.Path != value || (value != "/" && path.Clean(value) != strings.TrimSuffix(value, "/")) {
		return invalid("健康检查须为 /health 形式的路径，最多 256 字符，不包含地址、查询参数或片段")
	}
	return nil
}

// invalid 标识可直接展示的输入错误。
func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }
