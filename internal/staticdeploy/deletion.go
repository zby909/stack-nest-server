package staticdeploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"stacknest/server/internal/project"
)

type stagedDeletion struct {
	original string
	staged   string
	previous string
	drafts   string
}

// StageDelete 将站点移出公开目录，数据库删除失败时仍可原样恢复。
func (d *Deployer) StageDelete(ctx context.Context, slug string) (project.Deletion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := d.ensureSettled(slug); err != nil {
		return nil, err
	}
	deletion := d.deletion(slug)
	if _, err := os.Lstat(deletion.staged); err == nil {
		return nil, fmt.Errorf("unfinished deletion for %q; restart to recover", slug)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := os.Lstat(deletion.original); errors.Is(err, os.ErrNotExist) {
		// 即使当前文件缺失也保留删除标记，确保上一版最终得到清理。
		if err := os.MkdirAll(deletion.staged, 0o700); err != nil {
			return nil, err
		}
		return deletion, nil
	} else if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(deletion.staged), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(deletion.original, deletion.staged); err != nil {
		return nil, err
	}
	return deletion, nil
}

// Commit 清理已经不再对外提供的站点文件。
func (d *stagedDeletion) Commit() error {
	if d.staged == "" {
		return nil
	}
	if d.previous != "" {
		if err := os.RemoveAll(d.previous); err != nil {
			return err
		}
	}
	if d.drafts != "" {
		if err := os.RemoveAll(d.drafts); err != nil {
			return err
		}
	}
	return os.RemoveAll(d.staged)
}

// Rollback 恢复完整站点，恢复操作不依赖已取消的请求上下文。
func (d *stagedDeletion) Rollback() error {
	if d.staged == "" {
		return nil
	}
	entries, err := os.ReadDir(d.staged)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		// 原文件已缺失时暂存目录只有删除标记的作用，无需制造一个空站点。
		return os.Remove(d.staged)
	}
	if _, err := os.Lstat(d.original); err == nil {
		return fmt.Errorf("cannot restore site over existing directory %q", d.original)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(d.staged, d.original)
}

// RecoverDeletions 在服务启动前根据数据库记录恢复或清理中断的删除。
func (d *Deployer) RecoverDeletions(ctx context.Context, lookup func(context.Context, string) (project.Project, error)) error {
	entries, err := os.ReadDir(filepath.Join(d.config.DataDir, "trash"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || !project.ValidSlug(entry.Name()) {
			return fmt.Errorf("unexpected entry in deletion directory: %q", entry.Name())
		}
		deletion := d.deletion(entry.Name())
		_, lookupErr := lookup(ctx, entry.Name())
		switch {
		case lookupErr == nil:
			err = deletion.Rollback()
		case errors.Is(lookupErr, project.ErrNotFound):
			err = deletion.Commit()
		default:
			return fmt.Errorf("check pending deletion %q: %w", entry.Name(), lookupErr)
		}
		if err != nil {
			return fmt.Errorf("recover deletion %q: %w", entry.Name(), err)
		}
	}
	return nil
}

// deletion 定位同一数据卷内的公开目录与删除暂存目录。
func (d *Deployer) deletion(slug string) *stagedDeletion {
	return &stagedDeletion{
		drafts:   filepath.Join(d.config.DataDir, "drafts", slug),
		original: filepath.Join(d.config.DataDir, "sites", slug),
		staged:   filepath.Join(d.config.DataDir, "trash", slug),
		previous: filepath.Join(d.config.DataDir, "previous", slug),
	}
}
