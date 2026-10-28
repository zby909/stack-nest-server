package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// UpdateAs 校验归属和页面版本后，将新压缩包发布到原地址。
func (s *Service) UpdateAs(ctx context.Context, slug string, actor Actor, input UpdateInput) (CreatedProject, error) {
	if input.Preview {
		return s.stageDraft(ctx, slug, actor, input)
	}
	return s.revise(ctx, slug, actor, input, false, "")
}

// RollbackAs 将上一版与当前版互换，可再次回退以撤销本次操作。
func (s *Service) RollbackAs(ctx context.Context, slug string, actor Actor, expectedID string) (CreatedProject, error) {
	return s.revise(ctx, slug, actor, UpdateInput{ExpectedID: expectedID}, true, "")
}

// revise 串行协调文件切换与数据库事务，提交结果未知时保留恢复现场。
func (s *Service) revise(ctx context.Context, slug string, actor Actor, input UpdateInput, revert bool, draftID string) (CreatedProject, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	value, err := s.GetAs(ctx, slug, actor)
	if err != nil {
		return CreatedProject{}, err
	}
	ownerID := ""
	if !actor.IsAdmin {
		if actor.ID == "" || value.OwnerID != actor.ID {
			return CreatedProject{}, ErrNotFound
		}
		ownerID = actor.ID
	}
	if draftID != "" && CurrentID(value) == draftID {
		// 上次数据库提交后若访问标记写入失败，重试仍需修复，不能只返回成功。
		if err := s.publisher.SetPaused(slug, value.Status != "online"); err != nil {
			return CreatedProject{}, err
		}
		return CreatedProject{Project: value}, nil
	}
	if !revert && draftID == "" && containsDeployment(value, input.DeploymentID) {
		return CreatedProject{Project: value}, nil
	}
	if value.Draft != nil && draftID == "" {
		return CreatedProject{}, ErrDraftExists
	}
	if CurrentID(value) != input.ExpectedID || (draftID == "" && input.ExpectedID == "") {
		return CreatedProject{}, ErrVersionConflict
	}
	if draftID != "" {
		if value.Draft == nil || value.Draft.ID != draftID {
			return CreatedProject{}, ErrVersionConflict
		}
		input.DeploymentID, input.FileName, input.Notes = value.Draft.ID, value.Draft.FileName, value.Draft.Notes
	}
	if len(input.Notes) > 4096 {
		return CreatedProject{}, ErrInvalidNotes
	}
	if revert {
		if len(value.Deployments) < 2 {
			return CreatedProject{}, ErrNoPrevious
		}
		input.FileName, input.Notes, input.DeploymentID = value.Deployments[1].FileName, value.Deployments[1].Notes, value.Deployments[1].ID
	}
	// PostgreSQL 时间精度为微秒，确保快速连续发布仍有明确顺序。
	now := s.now().UTC().Truncate(time.Microsecond)
	latest := value.UpdatedAt
	if len(value.Deployments) > 0 && value.Deployments[0].CreatedAt.After(latest) {
		latest = value.Deployments[0].CreatedAt
	}
	if !now.After(latest) {
		now = latest.Truncate(time.Microsecond).Add(time.Microsecond)
	}
	next := Deployment{Notes: input.Notes, ID: newID("dep"), Status: "succeeded", FileName: input.FileName, CreatedAt: now, FinishedAt: now}
	if input.DeploymentID != "" {
		next.ID = input.DeploymentID
	}
	revision, err := s.publisher.StageRevision(ctx, RevisionInput{
		DraftID: draftID, Slug: slug, ProjectID: value.ID, FromID: input.ExpectedID, ToID: next.ID,
		FileName: input.FileName, Source: input.Source, Rollback: revert,
	})
	if err != nil {
		return CreatedProject{}, fmt.Errorf("stage project revision: %w", err)
	}
	if err := s.store.ReplaceDeployment(ctx, slug, ownerID, input.ExpectedID, next); err != nil {
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		stored, lookupErr := s.store.Get(checkCtx, slug)
		cancel()
		switch {
		case lookupErr == nil && stored.ID == value.ID && len(stored.Deployments) > 0 && stored.Deployments[0].ID == next.ID:
			slog.WarnContext(ctx, "revision commit confirmed after error", "slug", slug, "error", err)
		case lookupErr == nil && stored.ID == value.ID && CurrentID(stored) == input.ExpectedID,
			errors.Is(err, ErrNotFound), errors.Is(err, ErrVersionConflict):
			if restoreErr := revision.Rollback(); restoreErr != nil {
				return CreatedProject{}, fmt.Errorf("persist revision: %w; restore site: %v", err, restoreErr)
			}
			return CreatedProject{}, fmt.Errorf("persist revision: %w", err)
		default:
			return CreatedProject{}, fmt.Errorf("revision outcome unknown; restart recovery required: %w", err)
		}
	}
	if err := revision.Commit(); err != nil {
		slog.WarnContext(ctx, "revision committed; file cleanup deferred until restart", "slug", slug, "error", err)
	}
	value.UpdatedAt = now
	if len(value.Deployments) > 0 {
		value.Deployments = []Deployment{next, value.Deployments[0]}
	} else {
		value.Deployments = []Deployment{next}
	}
	if draftID != "" {
		value.Draft = nil
	}
	if value.Status == "draft" {
		value.Status = "online"
	}
	if err := s.publisher.SetPaused(slug, value.Status != "online"); err != nil {
		return CreatedProject{}, err
	}
	return CreatedProject{Project: value, Files: revision.Files()}, nil
}

// containsDeployment 识别响应丢失或收据写入前重启后的重试，不能重复替换当前版本。
func containsDeployment(value Project, id string) bool {
	if id == "" {
		return false
	}
	if value.Draft != nil && value.Draft.ID == id {
		return true
	}
	for _, deployment := range value.Deployments {
		if deployment.ID == id {
			return true
		}
	}
	return false
}
