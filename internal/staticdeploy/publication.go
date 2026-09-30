package staticdeploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"stacknest/server/internal/project"
)

// beginPublication 在公开文件前持久化标记，并排除并发的同名发布。
func (d *Deployer) beginPublication(slug string) error {
	if !project.ValidSlug(slug) {
		return project.ErrInvalidSlug
	}
	directory := filepath.Join(d.config.DataDir, "pending")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, slug), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return project.ErrAlreadyExists
	}
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// CompletePublication 清理已确认完成或撤销的发布标记。
func (d *Deployer) CompletePublication(slug string) error {
	if !project.ValidSlug(slug) {
		return project.ErrInvalidSlug
	}
	err := os.Remove(filepath.Join(d.config.DataDir, "pending", slug))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// RecoverPublications 根据元数据确认中断上传，再清理当前单实例遗留的临时目录。
func (d *Deployer) RecoverPublications(ctx context.Context, lookup func(context.Context, string) (project.Project, error)) error {
	entries, err := os.ReadDir(filepath.Join(d.config.DataDir, "pending"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !project.ValidSlug(entry.Name()) {
			return fmt.Errorf("invalid publication journal entry")
		}
		slug := entry.Name()
		_, lookupErr := lookup(ctx, slug)
		switch {
		case lookupErr == nil:
			if !isRegularFile(filepath.Join(d.config.DataDir, "sites", slug, "index.html")) {
				return fmt.Errorf("published project %q has no index; preserve journal for recovery", slug)
			}
		case errors.Is(lookupErr, project.ErrNotFound):
			if err := d.Delete(ctx, slug); err != nil {
				return err
			}
		default:
			return fmt.Errorf("verify pending publication %q: %w", slug, lookupErr)
		}
		if err := d.CompletePublication(slug); err != nil {
			return err
		}
		slog.InfoContext(ctx, "interrupted publication recovered", "slug", slug, "kept", lookupErr == nil)
	}
	for directory, prefixes := range map[string][]string{"tmp": {"upload-", "receive-"}, "sites": {".publish-"}} {
		entries, err := os.ReadDir(filepath.Join(d.config.DataDir, directory))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, prefix := range prefixes {
				if strings.HasPrefix(entry.Name(), prefix) {
					if err := os.RemoveAll(filepath.Join(d.config.DataDir, directory, entry.Name())); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
