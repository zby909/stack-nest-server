package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	slugRE      = regexp.MustCompile(`[^a-z0-9-]+`)
	validSlugRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,50}[a-z0-9])?$`)
)

// MaxSlugLength 为站点域名前缀 stack-nest- 预留 DNS 标签长度。
const MaxSlugLength = 52

// Service 编排项目持久化和静态文件发布。
type Service struct {
	store     Store
	publisher Publisher
	now       func() time.Time
	writeMu   sync.Mutex // 单实例串行执行写操作，避免同一 slug 的创建与删除交错。
}

// NewService 创建 Project 业务服务。
func NewService(store Store, publisher Publisher) *Service {
	return &Service{
		store:     store,
		publisher: publisher,
		now:       time.Now,
	}
}

// List 返回所有项目。
func (s *Service) List(ctx context.Context) ([]Project, error) {
	return s.store.List(ctx)
}

// Get 按 slug 查询项目。
func (s *Service) Get(ctx context.Context, slug string) (Project, error) {
	return s.store.Get(ctx, slug)
}

// Create 校验项目并完成静态文件与元数据发布。
func (s *Service) Create(ctx context.Context, input CreateInput) (CreatedProject, error) {
	if input.OwnerID == "" {
		return CreatedProject{}, ErrMissingOwner
	}
	name, description, err := normalizeMetadata(input.Name, input.Description)
	if err != nil {
		return CreatedProject{}, err
	}

	slug := input.Slug
	if slug == "" {
		slug = Slugify(name)
		if slug == "" {
			slug = "project-" + uuid.NewString()
		}
	}
	if !ValidSlug(slug) {
		return CreatedProject{}, ErrInvalidSlug
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if existing, err := s.store.Get(ctx, slug); err == nil {
		if existing.OwnerID == input.OwnerID && containsDeployment(existing, input.DeploymentID) {
			return CreatedProject{Project: existing}, nil
		}
		return CreatedProject{}, ErrAlreadyExists
	} else if !errors.Is(err, ErrNotFound) {
		return CreatedProject{}, fmt.Errorf("check existing project: %w", err)
	}

	if len(input.Notes) > 4096 {
		return CreatedProject{}, ErrInvalidNotes
	}
	id := input.DeploymentID
	if id == "" {
		id = newID("dep")
	}
	var files PublishedFiles
	if input.Preview {
		if err := s.publisher.SetPaused(slug, true); err != nil {
			return CreatedProject{}, err
		}
		files, err = s.publisher.PrepareDraft(ctx, slug, id, input.FileName, input.Source)
	} else {
		files, err = s.publisher.Publish(ctx, slug, input.FileName, input.Source)
	}
	if err != nil {
		if errors.Is(err, ErrAlreadyExists) {
			return CreatedProject{}, ErrAlreadyExists
		}
		return CreatedProject{}, fmt.Errorf("publish static site: %w", err)
	}

	now := s.now().UTC().Truncate(time.Microsecond)
	deployment := Deployment{
		ID:         id,
		Notes:      strings.TrimSpace(input.Notes),
		Status:     "succeeded",
		FileName:   input.FileName,
		CreatedAt:  now,
		FinishedAt: now,
	}
	if input.DeploymentID != "" {
		deployment.ID = input.DeploymentID
	}
	value := Project{
		ID:          newID("prj"),
		OwnerID:     input.OwnerID,
		OwnerEmail:  input.OwnerEmail,
		Name:        name,
		Slug:        slug,
		Description: description,
		Status:      "online",
		CreatedAt:   now,
		UpdatedAt:   now,
		Deployments: []Deployment{deployment},
	}

	if input.Preview {
		deployment.Status = "draft"
		value.Status, value.Draft, value.Deployments = "draft", &deployment, nil
	}
	if err := s.store.Create(ctx, value); err != nil {
		// 提交报错可能发生在事务已经成功之后，先确认结果再决定是否清理。
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		stored, lookupErr := s.store.Get(checkCtx, slug)
		cancel()
		if lookupErr == nil && stored.ID == value.ID {
			slog.WarnContext(ctx, "project commit confirmed after error", "slug", slug, "error", err)
		} else {
			if !errors.Is(lookupErr, ErrNotFound) {
				return CreatedProject{}, fmt.Errorf("publication outcome unknown; restart recovery required: %w", err)
			}
			var cleanupErr error
			if input.Preview {
				cleanupErr = s.publisher.RemoveDraft(slug, id)
			} else {
				cleanupErr = s.publisher.Delete(context.WithoutCancel(ctx), slug)
			}
			if cleanupErr != nil {
				return CreatedProject{}, fmt.Errorf("persist project failed: %v; cleanup published site: %w", err, cleanupErr)
			}
			if cleanupErr := s.publisher.CompletePublication(slug); cleanupErr != nil {
				return CreatedProject{}, fmt.Errorf("clear failed publication: %w", cleanupErr)
			}
			return CreatedProject{}, fmt.Errorf("persist project: %w", err)
		}
	}
	if err := s.publisher.CompletePublication(slug); err != nil {
		slog.WarnContext(ctx, "publication complete; journal cleanup deferred", "slug", slug, "error", err)
	}
	return CreatedProject{Project: value, Files: files}, nil
}

// ValidSlug 供发布及恢复流程统一验证安全目录名。
func ValidSlug(slug string) bool { return validSlugRE.MatchString(slug) }

// DeleteAs 在文件暂存和数据库删除期间校验项目归属。
func (s *Service) DeleteAs(ctx context.Context, slug string, actor Actor) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	value, err := s.store.Get(ctx, slug)
	if err != nil {
		return err
	}
	ownerID := ""
	if !actor.IsAdmin {
		if actor.ID == "" || value.OwnerID != actor.ID {
			return ErrNotFound
		}
		ownerID = actor.ID
	}
	deletion, err := s.publisher.StageDelete(ctx, slug)
	if err != nil {
		return fmt.Errorf("stage static site deletion: %w", err)
	}
	if err := s.store.Delete(ctx, slug, ownerID); errors.Is(err, ErrNotFound) {
		if restoreErr := deletion.Rollback(); restoreErr != nil {
			return fmt.Errorf("project ownership changed; restore static site: %w", restoreErr)
		}
		return ErrNotFound
	} else if err != nil {
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_, lookupErr := s.store.Get(checkCtx, slug)
		cancel()
		if lookupErr == nil {
			if restoreErr := deletion.Rollback(); restoreErr != nil {
				return fmt.Errorf("delete project metadata: %w; restore static site: %w", err, restoreErr)
			}
			return fmt.Errorf("delete project metadata: %w", err)
		}
		if !errors.Is(lookupErr, ErrNotFound) {
			return fmt.Errorf("deletion outcome unknown; preserve trash for restart recovery: %w", err)
		}
		slog.WarnContext(ctx, "project deletion confirmed after error", "slug", slug, "error", err)
	}
	if err := deletion.Commit(); err != nil {
		// 项目已删除且文件不再对外提供；启动恢复会重试清理暂存目录。
		slog.WarnContext(ctx, "project deleted; file cleanup deferred until restart", "slug", slug, "error", err)
	}
	return nil
}

// Slugify 将项目名规范化为 URL 安全 slug。
func Slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, " ", "-")
	value = slugRE.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	if len(value) > MaxSlugLength {
		value = strings.Trim(value[:MaxSlugLength], "-")
	}
	return value
}

// newID 生成带业务前缀的唯一标识。
func newID(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, uuid.NewString())
}
