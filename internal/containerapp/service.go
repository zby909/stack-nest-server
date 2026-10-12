package containerapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"stacknest/server/internal/activity"
	"stacknest/server/internal/storage"
)

// Service 在所有权校验后加密配置，并执行可恢复的串行任务队列。
type Service struct {
	store    Store
	runtime  Runtime
	secret   string
	guard    *storage.Guard
	activity *activity.Store
}

// New 创建单实例应用服务，运行器未配置时保持静态功能可用。
func New(store Store, runtime Runtime, secret string, guard *storage.Guard, events *activity.Store) *Service {
	return &Service{store: store, runtime: runtime, secret: secret, guard: guard, activity: events}
}

// Runtime 返回受限的站点代理运行器。
func (s *Service) Runtime() Runtime { return s.runtime }

// view 从密文填充可公开字段，永不返回环境变量值和仓库密码。
func (s *Service) view(a App) (App, error) {
	spec, err := open(s.secret, a.ID, a.Sealed)
	if err != nil {
		return a, err
	}
	a.Image, a.Port, a.MemoryMiB, a.CPUs, a.DataPath = spec.Image, spec.Port, spec.MemoryMiB, spec.CPUs, spec.DataPath
	a.HealthPath = spec.HealthPath
	a.EnvKeys = []string{}
	for key := range spec.Env {
		a.EnvKeys = append(a.EnvKeys, key)
	}
	sort.Strings(a.EnvKeys)
	a.HasRegistry = spec.Registry != nil
	return a, nil
}

// List 仅返回调用者能管理的应用。
func (s *Service) List(ctx context.Context, actor Actor) ([]App, error) {
	values, err := s.store.Apps(ctx, actor)
	if err != nil {
		return nil, err
	}
	for i := range values {
		values[i], err = s.view(values[i])
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

// Get 同时验证规范编号和归属。
func (s *Service) Get(ctx context.Context, id string, actor Actor) (App, error) {
	if !ValidID(id) {
		return App{}, ErrNotFound
	}
	a, err := s.store.App(ctx, id, actor)
	if err != nil {
		return a, err
	}
	return s.view(a)
}

// Save 先检查资源与磁盘，再保存任务；编辑时保留未替换的敏感信息。
func (s *Service) Save(ctx context.Context, id string, actor Actor, input Input, generation int64) (App, error) {
	create := id == ""
	var a App
	if create {
		a.ID = uuid.NewString()
		a.Slug = strings.TrimSpace(input.Slug)
		a.OwnerID = actor.ID
	} else {
		var err error
		a, err = s.Get(ctx, id, actor)
		if err != nil {
			return a, err
		}
		if a.Generation != generation {
			return a, ErrBusy
		}
		previous, err := open(s.secret, id, a.Sealed)
		if err != nil {
			return a, err
		}
		if !input.ReplaceEnv {
			input.Env = previous.Env
		}
		if !input.ClearRegistry && input.Registry == nil && registryHost(input.Image) == registryHost(previous.Image) {
			input.Registry = previous.Registry
		}
		if input.Slug != "" && input.Slug != a.Slug {
			return a, invalid("应用标识创建后不可修改")
		}
	}
	input.Name = strings.TrimSpace(input.Name)
	if utf8.RuneCountInString(input.Name) < 1 || utf8.RuneCountInString(input.Name) > 100 || !ValidSlug(a.Slug) {
		return a, invalid("名称须为 1–100 字符，标识须为 1–32 位小写字母、数字或连字符")
	}
	input.Image = strings.TrimSpace(input.Image)
	if input.ClearRegistry {
		input.Registry = nil
	}
	if err := input.Spec.Validate(); err != nil {
		return a, err
	}
	if err := s.runtime.Health(ctx); err != nil {
		return a, ErrUnavailable
	}
	if err := s.guard.Check(2 * MaxImageBytes); err != nil {
		return a, invalid("磁盘可用空间不足，已暂停部署")
	}
	var err error
	a.Sealed, err = seal(s.secret, a.ID, input.Spec)
	if err != nil {
		return a, err
	}
	a.Name, a.MemoryMiB = input.Name, input.MemoryMiB
	if err = s.store.SaveApp(ctx, a, actor, create); err != nil {
		return a, err
	}
	return s.Get(ctx, a.ID, actor)
}

// Queue 检查持久化代次并排队，不把长操作交给浏览器连接。
func (s *Service) Queue(ctx context.Context, id string, actor Actor, action string, generation int64) (App, error) {
	switch action {
	case "start", "stop", "restart", "delete":
	default:
		return App{}, invalid("应用操作无效")
	}
	if !ValidID(id) {
		return App{}, ErrNotFound
	}
	if err := s.runtime.Health(ctx); err != nil {
		return App{}, ErrUnavailable
	}
	a, err := s.store.QueueApp(ctx, id, actor, action, generation)
	if err != nil {
		return a, err
	}
	return s.view(a)
}

// State 只检查已授权应用的容器状态，不泄漏 Docker 的完整 inspect。
func (s *Service) State(ctx context.Context, a App) (Result, error) {
	return s.runtime.State(ctx, a.ID, a.ContainerID)
}

// Logs 返回有界日志，并额外遮盖平台保存的敏感配置值。
func (s *Service) Logs(ctx context.Context, a App) (string, error) {
	text, err := s.runtime.Logs(ctx, a.ID, a.ContainerID)
	if err != nil {
		return "", err
	}
	spec, err := open(s.secret, a.ID, a.Sealed)
	if err != nil {
		return "", err
	}
	specs := []Spec{spec}
	// 更新期间后台保存候选配置，但日志仍可能来自使用旧配置的容器。
	if a.ActiveSealed != "" && a.ActiveSealed != a.Sealed {
		active, err := open(s.secret, a.ID, a.ActiveSealed)
		if err != nil {
			return "", err
		}
		specs = append(specs, active)
	}
	return redactLog(text, specs...), nil
}

// Resolve 为公开应用域名查询目标，结果不经过后台会话鉴权。
func (s *Service) Resolve(ctx context.Context, slug string) (App, error) {
	if !ValidSlug(slug) {
		return App{}, ErrNotFound
	}
	values, err := s.store.Apps(ctx, Actor{Admin: true})
	if err != nil {
		return App{}, err
	}
	for _, a := range values {
		if a.Slug == slug {
			return a, nil
		}
	}
	return App{}, ErrNotFound
}

// Run 启动时恢复任务；空闲时修复迁移后丢失的已运行容器。
func (s *Service) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		recoverCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.store.RecoverApps(recoverCtx)
		cancel()
		if err == nil {
			break
		}
		slog.Error("recover app queue", "error", err)
		if !waitWorkerRetry(ctx) {
			return
		}
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastReconcile := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		a, err := s.store.ClaimApp(ctx)
		if err == nil {
			s.execute(ctx, a)
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			slog.Error("claim app operation", "error", err)
		}
		if time.Since(lastReconcile) > time.Minute {
			s.reconcile(ctx)
			lastReconcile = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// execute 在取消时保留未完成任务，下次启动重试；不记录敏感 spec。
func (s *Service) execute(ctx context.Context, a App) {
	started := time.Now()
	spec, err := open(s.secret, a.ID, a.Sealed)
	result := Result{ContainerID: a.ContainerID, ImageID: a.ImageID, State: "unknown"}
	if err == nil {
		a.MemoryMiB = spec.MemoryMiB
		var limits LimitsView
		limits, err = s.Limits(ctx)
		if err == nil {
			task, cancel := context.WithTimeout(ctx, 10*time.Minute)
			result, err = s.runtime.Operate(task, Operation{ID: a.ID, Generation: a.Generation, Action: a.Action, Spec: spec, ImageID: a.ImageID, Limits: &limits.Limits})
			cancel()
		}
	}
	if ctx.Err() != nil {
		return
	}
	message := ""
	if err != nil {
		message = fmt.Sprint(err)
		if result.ContainerID == "" {
			result.ContainerID = a.ContainerID
			result.ImageID = a.ImageID
		}
		// 更新失败恢复原有配置，保留运行器已回退的旧容器。
		if a.Action == "deploy" && a.ActiveSealed != "" {
			a.Sealed = a.ActiveSealed
			if old, openErr := open(s.secret, a.ID, a.Sealed); openErr == nil {
				a.MemoryMiB = old.MemoryMiB
			}
		}
	} else if a.Action == "deploy" || a.Action == "start" {
		a.ActiveSealed = a.Sealed
	}
	// 容器操作已完成；数据库短暂不可用时只重试提交同一结果。
	for {
		finish, cancel := context.WithTimeout(ctx, 10*time.Second)
		saveErr := s.store.CompleteApp(finish, a, result, message)
		cancel()
		if saveErr == nil {
			break
		}
		slog.Error("complete app operation", "id", a.ID, "error", saveErr)
		if !waitWorkerRetry(ctx) {
			return
		}
	}
	event := activity.Event{ID: uuid.NewString(), CreatedAt: started.UTC(), Action: "app-" + a.Action, Actor: a.ActorEmail, ActorID: a.ActorID, ProjectOwnerID: a.OwnerID, Slug: a.Slug, Status: "succeeded", Message: "应用操作已完成", DurationMS: time.Since(started).Milliseconds()}
	if err != nil {
		event.Status, event.Message = "failed", message
	}
	if s.activity != nil {
		if logErr := s.activity.Append(event); logErr != nil {
			slog.Error("append app activity", "error", logErr)
		}
	}
}

// waitWorkerRetry 等待短暂数据库故障恢复，并在关闭时立即退出，避免失败忙循环。
func waitWorkerRetry(ctx context.Context) bool {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

// reconcile 仅重建数据库仍要求运行的丢失容器，不启动用户主动停止的应用。
func (s *Service) reconcile(ctx context.Context) {
	values, err := s.store.Apps(ctx, Actor{Admin: true})
	if err != nil {
		return
	}
	for _, a := range values {
		if a.Status != "ready" || a.ContainerID == "" {
			continue
		}
		result, err := s.runtime.State(ctx, a.ID, a.ContainerID)
		if errors.Is(err, ErrNotFound) || err == nil && result.State == "missing" {
			_, _ = s.store.QueueApp(ctx, a.ID, Actor{ID: a.OwnerID, Email: a.OwnerEmail, Admin: true}, "start", a.Generation)
		}
	}
}

// registryHost 为未带域名的 Docker Hub 地址返回统一仓库标识。
func registryHost(image string) string {
	head, _, found := strings.Cut(image, "/")
	if found && (strings.ContainsAny(head, ".:") || head == "localhost") {
		return head
	}
	return "https://index.docker.io/v1/"
}
