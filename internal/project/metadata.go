package project

import (
	"context"
	"strings"
	"time"
)

// normalizeMetadata 与上传字段的 UTF-8 字节限制保持一致。
func normalizeMetadata(name, description string) (string, string, error) {
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if name == "" || len(name) > 256 {
		return "", "", ErrInvalidName
	}
	if len(description) > 4096 {
		return "", "", ErrInvalidDescription
	}
	return name, description, nil
}

// EditAs 按归属和更新时间修改名称、说明，保留站点地址与部署文件。
func (s *Service) EditAs(ctx context.Context, slug string, actor Actor, input MetadataInput) (Project, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	value, err := s.store.Get(ctx, slug)
	if err != nil {
		return Project{}, err
	}
	if !actor.IsAdmin && (actor.ID == "" || actor.ID != value.OwnerID) {
		return Project{}, ErrNotFound
	}
	expected := input.ExpectedAt.UTC().Truncate(time.Microsecond)
	if expected.IsZero() || !value.UpdatedAt.UTC().Truncate(time.Microsecond).Equal(expected) {
		return Project{}, ErrVersionConflict
	}
	name, description, err := normalizeMetadata(input.Name, input.Description)
	if err != nil {
		return Project{}, err
	}
	value.Name, value.Description = name, description
	value.UpdatedAt = s.now().UTC().Truncate(time.Microsecond)
	if !value.UpdatedAt.After(expected) {
		value.UpdatedAt = expected.Add(time.Microsecond)
	}
	if err := s.store.UpdateMetadata(ctx, value, expected); err != nil {
		return Project{}, err
	}
	return value, nil
}

// ExportAs 在短暂的文件快照阶段串行校验归属与版本，完成后允许继续更新项目。
func (s *Service) ExportAs(ctx context.Context, slug string, actor Actor, deploymentID string) (ExportedProject, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return ExportedProject{}, err
	}
	value, err := s.store.Get(ctx, slug)
	if err != nil {
		return ExportedProject{}, err
	}
	if !actor.IsAdmin && (actor.ID == "" || actor.ID != value.OwnerID) {
		return ExportedProject{}, ErrNotFound
	}
	if value.Draft != nil && value.Draft.ID == deploymentID {
		archive, err := s.publisher.ExportAt(ctx, slug, deploymentID)
		if err != nil {
			return ExportedProject{}, err
		}
		return ExportedProject{Archive: archive, Deployment: *value.Draft, OwnerID: value.OwnerID}, nil
	}
	for index, deployment := range value.Deployments {
		if index > 1 {
			break
		}
		if deployment.ID == deploymentID && deploymentID != "" {
			archive, err := s.publisher.Export(ctx, slug, index == 1)
			if err != nil {
				return ExportedProject{}, err
			}
			return ExportedProject{Archive: archive, Deployment: deployment, OwnerID: value.OwnerID}, nil
		}
	}
	return ExportedProject{}, ErrVersionConflict
}
