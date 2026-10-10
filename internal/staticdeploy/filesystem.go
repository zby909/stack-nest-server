package staticdeploy

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"

	"stacknest/server/internal/project"
)

// Measure 只统计已完成事务中的指定版本，不访问归档、日志或其他项目目录。
func (d *Deployer) Measure(ctx context.Context, slug string, previous bool) (project.PublishedFiles, error) {
	slot := "sites"
	if previous {
		slot = "previous"
	}
	return d.MeasureAt(ctx, slug, slot)
}

// MeasureAt 对草稿、当前版和上一版使用一致的安全校验。
func (d *Deployer) MeasureAt(ctx context.Context, slug, slot string) (project.PublishedFiles, error) {
	if err := ctx.Err(); err != nil {
		return project.PublishedFiles{}, err
	}
	if err := d.ensureSettled(slug); err != nil {
		return project.PublishedFiles{}, err
	}
	directory, err := d.versionDirectory(slug, slot)
	if err != nil {
		return project.PublishedFiles{}, err
	}
	index, err := os.Lstat(filepath.Join(directory, "index.html"))
	if err != nil || !index.Mode().IsRegular() {
		return project.PublishedFiles{}, project.ErrUsageUnavailable
	}
	return measureSite(ctx, directory)
}

// measureSite 统计普通文件的逻辑字节数，拒绝缺失站点和链接，避免报告不完整的占用。
func measureSite(ctx context.Context, root string) (project.PublishedFiles, error) {
	var result project.PublishedFiles
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, project.ErrUsageUnavailable
	}
	directory, err := os.OpenRoot(root)
	if err != nil {
		return result, err
	}
	defer directory.Close()
	err = fs.WalkDir(directory.FS(), ".", func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return project.ErrUsageUnavailable
		}
		result.Bytes += info.Size()
		result.Count++
		return nil
	})
	if err != nil {
		return project.PublishedFiles{}, err
	}
	return result, err
}
