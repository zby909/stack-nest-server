package project

import (
	"context"
	"errors"
	"fmt"
)

// FileUsageAs 在文件切换锁内核对归属和部署编号，避免把新版本大小算到旧版本上。
func (s *Service) FileUsageAs(ctx context.Context, expected Project, actor Actor) (map[string]PublishedFiles, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	current, err := s.store.Get(ctx, expected.Slug)
	if err != nil {
		return nil, err
	}
	if !actor.IsAdmin && (actor.ID == "" || current.OwnerID != actor.ID) {
		return nil, ErrNotFound
	}
	if current.ID != expected.ID || len(current.Deployments) != len(expected.Deployments) {
		return nil, ErrVersionConflict
	}
	for i, deployment := range current.Deployments {
		if i > 1 || deployment.ID != expected.Deployments[i].ID {
			return nil, ErrVersionConflict
		}
	}
	if (current.Draft == nil) != (expected.Draft == nil) || current.Draft != nil && current.Draft.ID != expected.Draft.ID {
		return nil, ErrVersionConflict
	}
	usage := make(map[string]PublishedFiles, len(current.Deployments))
	var failures []error
	for i, deployment := range current.Deployments {
		files, err := s.publisher.Measure(ctx, current.Slug, i == 1)
		if err != nil {
			failures = append(failures, fmt.Errorf("measure deployment %s: %w", deployment.ID, err))
			continue
		}
		usage[deployment.ID] = files
	}
	if current.Draft != nil {
		files, err := s.publisher.MeasureAt(ctx, current.Slug, current.Draft.ID)
		if err != nil {
			failures = append(failures, err)
		} else {
			usage[current.Draft.ID] = files
		}
	}
	return usage, errors.Join(failures...)
}
