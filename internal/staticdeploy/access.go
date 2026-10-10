package staticdeploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"stacknest/server/internal/project"
)

// SetPaused 使用独立于站点内容的目录标记阻止访问，上传文件无法改动这个标记。
func (d *Deployer) SetPaused(slug string, paused bool) error {
	if !project.ValidSlug(slug) {
		return project.ErrInvalidSlug
	}
	parent := filepath.Join(d.config.DataDir, "paused")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	path := filepath.Join(parent, slug)
	if paused {
		if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("invalid site access marker: expected directory")
		}
	} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(parent)
}

// RecoverAccess 按持久化状态重建访问标记，保留未知项目的关闭标记直到重新发布。
func (d *Deployer) RecoverAccess(ctx context.Context, list func(context.Context) ([]project.Project, error)) error {
	values, err := list(ctx)
	if err != nil {
		return err
	}
	for _, value := range values {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.SetPaused(value.Slug, value.Status != "online"); err != nil {
			return err
		}
	}
	return nil
}
