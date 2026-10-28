package project

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// GetAs 统一按所有者或管理员身份查询项目，不透露其他用户的项目是否存在。
func (s *Service) GetAs(ctx context.Context, slug string, actor Actor) (Project, error) {
	value, err := s.store.Get(ctx, slug)
	if err != nil {
		return Project{}, err
	}
	if !actor.IsAdmin && (actor.ID == "" || value.OwnerID != actor.ID) {
		return Project{}, ErrNotFound
	}
	if actor.ProjectID != "" && value.ID != actor.ProjectID {
		return Project{}, ErrNotFound
	}
	return value, nil
}

// CurrentID 返回正式版本编号，首次发布前为空。
func CurrentID(value Project) string {
	if len(value.Deployments) == 0 {
		return ""
	}
	return value.Deployments[0].ID
}

// CheckDraftTarget 同时核对正式版和明确指定的旧草稿，未指定时继续禁止覆盖。
func CheckDraftTarget(value Project, expectedID, replaceDraftID string) error {
	if CurrentID(value) != expectedID {
		return ErrVersionConflict
	}
	if replaceDraftID != "" {
		if value.Draft == nil || value.Draft.ID != replaceDraftID {
			return ErrVersionConflict
		}
	} else if value.Draft != nil {
		return ErrDraftExists
	}
	return nil
}

// stageDraft 验证单一草稿和归属，持久化失败时仅清理可确认未提交的文件。
func (s *Service) stageDraft(ctx context.Context, slug string, actor Actor, input UpdateInput) (CreatedProject, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	value, err := s.GetAs(ctx, slug, actor)
	if err != nil {
		return CreatedProject{}, err
	}
	if input.ReplaceDraftID != "" && input.ReplaceDraftID == input.DeploymentID {
		return CreatedProject{}, ErrVersionConflict
	}
	if containsDeployment(value, input.DeploymentID) {
		return CreatedProject{Project: value}, nil
	}
	if err := CheckDraftTarget(value, input.ExpectedID, input.ReplaceDraftID); err != nil {
		return CreatedProject{}, err
	}
	if len(input.Notes) > 4096 {
		return CreatedProject{}, ErrInvalidNotes
	}
	id := input.DeploymentID
	if id == "" {
		id = newID("dep")
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	if !now.After(value.UpdatedAt) {
		now = value.UpdatedAt.Add(time.Microsecond)
	}
	next := Deployment{ID: id, Status: "draft", FileName: input.FileName, Notes: strings.TrimSpace(input.Notes), CreatedAt: now, FinishedAt: now}
	files, err := s.publisher.PrepareDraft(ctx, slug, id, input.FileName, input.Source)
	if err != nil {
		return CreatedProject{}, err
	}
	if err := s.store.SaveDraft(ctx, slug, value.OwnerID, input.ExpectedID, input.ReplaceDraftID, next); err != nil {
		check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		stored, lookupErr := s.store.Get(check, slug)
		cancel()
		if lookupErr != nil || stored.ID != value.ID {
			return CreatedProject{}, fmt.Errorf("draft outcome unknown; preserve files for recovery: %w", err)
		}
		if !containsDeployment(stored, id) {
			if cleanupErr := s.publisher.RemoveDraft(slug, id); cleanupErr != nil {
				return CreatedProject{}, fmt.Errorf("save draft: %w; cleanup: %v", err, cleanupErr)
			}
			return CreatedProject{}, err
		}
		value = stored
	} else {
		value.Draft, value.UpdatedAt = &next, now
	}
	if oldID := input.ReplaceDraftID; oldID != "" && !containsDeployment(value, oldID) {
		if err := s.publisher.RemoveDraft(slug, oldID); err != nil {
			slog.WarnContext(ctx, "replaced draft cleanup deferred", "slug", slug, "error", err)
		}
	}
	return CreatedProject{Project: value, Files: files}, nil
}

// PublishDraftAs 明确发布指定草稿，重复提交同一个已发布版本时返回当前状态。
func (s *Service) PublishDraftAs(ctx context.Context, slug string, actor Actor, draftID, expectedID string) (CreatedProject, error) {
	if draftID == "" {
		return CreatedProject{}, ErrVersionConflict
	}
	return s.revise(ctx, slug, actor, UpdateInput{ExpectedID: expectedID}, false, draftID)
}

// DiscardDraftAs 先删除数据库引用，再清理私有文件；中断后的残留由启动恢复处理。
func (s *Service) DiscardDraftAs(ctx context.Context, slug string, actor Actor, draftID string) (Project, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	value, err := s.GetAs(ctx, slug, actor)
	if err != nil {
		return Project{}, err
	}
	if value.Draft == nil || draftID == "" || value.Draft.ID != draftID {
		return Project{}, ErrVersionConflict
	}
	if err := s.store.DeleteDraft(ctx, slug, value.OwnerID, draftID); err != nil {
		return Project{}, err
	}
	value.Draft = nil
	if err := s.publisher.RemoveDraft(slug, draftID); err != nil {
		slog.WarnContext(ctx, "draft cleanup deferred", "slug", slug, "error", err)
	}
	return value, nil
}

// VersionSlot 仅把数据库保留的版本映射到内部目录，外部参数不能直接指定目录。
func VersionSlot(value Project, id string) (string, bool) {
	if id == "" {
		return "", false
	}
	if value.Draft != nil && value.Draft.ID == id {
		return id, true
	}
	for index, version := range value.Deployments {
		if index > 1 {
			break
		}
		if version.ID == id {
			if index == 0 {
				return "sites", true
			}
			return "previous", true
		}
	}
	return "", false
}

// OpenPreview 在版本锁内打开目录句柄，后续发布切换也不会让同一请求读到另一版。
func (s *Service) OpenPreview(ctx context.Context, id string) (*os.Root, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	values, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, value := range values {
		if slot, ok := VersionSlot(value, id); ok && value.Status != "paused" {
			return s.publisher.OpenVersion(value.Slug, slot)
		}
	}
	return nil, ErrNotFound
}
