package project

import (
	"context"
	"fmt"
	"time"
)

// SetAccessAs 在关闭访问的保护下修改状态；提交不确定时保持关闭，启动时按数据库恢复。
func (s *Service) SetAccessAs(ctx context.Context, slug string, actor Actor, paused bool, expectedAt time.Time) (Project, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	value, err := s.store.Get(ctx, slug)
	if err != nil {
		return Project{}, err
	}
	if !actor.IsAdmin && (actor.ID == "" || actor.ID != value.OwnerID) {
		return Project{}, ErrNotFound
	}
	if len(value.Deployments) == 0 {
		return Project{}, ErrVersionConflict
	}
	expected := expectedAt.UTC().Truncate(time.Microsecond)
	if expected.IsZero() || !value.UpdatedAt.UTC().Truncate(time.Microsecond).Equal(expected) {
		return Project{}, ErrVersionConflict
	}
	status := "online"
	if paused {
		status = "paused"
	}
	if value.Status != status {
		// 先阻止静态入口，数据库或文件系统出错时不能意外公开已暂停的文件。
		if err := s.publisher.SetPaused(slug, true); err != nil {
			return Project{}, fmt.Errorf("close site access: %w", err)
		}
		value.Status = status
		value.UpdatedAt = s.now().UTC().Truncate(time.Microsecond)
		if !value.UpdatedAt.After(expected) {
			value.UpdatedAt = expected.Add(time.Microsecond)
		}
		if err := s.store.UpdateMetadata(ctx, value, expected); err != nil {
			checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			stored, lookupErr := s.store.Get(checkCtx, slug)
			cancel()
			if lookupErr != nil || stored.ID != value.ID {
				return Project{}, fmt.Errorf("access outcome unknown; restart recovery required: %w", err)
			}
			if stored.Status != status || !stored.UpdatedAt.Equal(value.UpdatedAt) {
				if restoreErr := s.publisher.SetPaused(slug, stored.Status != "online"); restoreErr != nil {
					return Project{}, fmt.Errorf("restore site access after failed save: %w", restoreErr)
				}
				return Project{}, err
			}
		}
	}
	if err := s.publisher.SetPaused(slug, paused); err != nil {
		return Project{}, fmt.Errorf("apply site access; refresh and retry required: %w", err)
	}
	return value, nil
}
