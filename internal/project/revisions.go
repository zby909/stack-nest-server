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
	return s.revise(ctx, slug, actor, input, false)
}

// RollbackAs 将上一版与当前版互换，可再次回退以撤销本次操作。
func (s *Service) RollbackAs(ctx context.Context, slug string, actor Actor, expectedID string) (CreatedProject, error) {
	return s.revise(ctx, slug, actor, UpdateInput{ExpectedID: expectedID}, true)
}

// revise 串行协调文件切换与数据库事务，提交结果未知时保留恢复现场。
func (s *Service) revise(ctx context.Context, slug string, actor Actor, input UpdateInput, revert bool) (CreatedProject, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	value, err := s.store.Get(ctx, slug)
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
	if len(value.Deployments) == 0 || input.ExpectedID == "" || value.Deployments[0].ID != input.ExpectedID {
		return CreatedProject{}, ErrVersionConflict
	}
	if revert {
		if len(value.Deployments) < 2 {
			return CreatedProject{}, ErrNoPrevious
		}
		input.FileName = value.Deployments[1].FileName
	}
	// PostgreSQL 时间精度为微秒，确保快速连续发布仍有明确顺序。
	now := s.now().UTC().Truncate(time.Microsecond)
	if !now.After(value.Deployments[0].CreatedAt) {
		now = value.Deployments[0].CreatedAt.Truncate(time.Microsecond).Add(time.Microsecond)
	}
	next := Deployment{ID: newID("dep"), Status: "succeeded", FileName: input.FileName, CreatedAt: now, FinishedAt: now}
	revision, err := s.publisher.StageRevision(ctx, RevisionInput{
		Slug: slug, ProjectID: value.ID, FromID: input.ExpectedID, ToID: next.ID,
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
		case lookupErr == nil && stored.ID == value.ID && len(stored.Deployments) > 0 && stored.Deployments[0].ID == input.ExpectedID,
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
	value.Deployments = []Deployment{next, value.Deployments[0]}
	return CreatedProject{Project: value, Files: revision.Files()}, nil
}
