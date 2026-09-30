package staticdeploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

// prepareSite 在私有临时目录完成限额检查、解压和入口校验，不触碰在线站点。
func (d *Deployer) prepareSite(ctx context.Context, fileName string, source io.Reader) (root, tempDir string, stats project.PublishedFiles, resultErr error) {
	if err := d.config.Guard.Check(storage.MaxArchiveBytes + d.config.MaxBytes + (32 << 20)); err != nil {
		return "", "", stats, err
	}
	tempRoot := filepath.Join(d.config.DataDir, "tmp")
	if err := os.MkdirAll(tempRoot, 0o755); err != nil {
		return "", "", stats, err
	}
	tempDir, err := os.MkdirTemp(tempRoot, "upload-*")
	if err != nil {
		return "", "", stats, err
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(tempDir)
		}
	}()
	archivePath := filepath.Join(tempDir, "source.archive")
	if err := d.saveArchive(ctx, source, archivePath); err != nil {
		return "", tempDir, stats, err
	}
	if err := d.config.Guard.Check(d.config.MaxBytes + (32 << 20)); err != nil {
		return "", tempDir, stats, err
	}
	releaseDir := filepath.Join(tempDir, "release")
	if err := extractArchiveGuarded(ctx, d.config.Guard, archivePath, fileName, releaseDir, d.config.MaxFiles, d.config.MaxBytes); err != nil {
		return "", tempDir, stats, fmt.Errorf("压缩包解压失败: %w", err)
	}
	root, err = detectSiteRoot(releaseDir)
	if err != nil {
		return "", tempDir, stats, err
	}
	stats, err = measureSite(ctx, releaseDir)
	if err != nil {
		return "", tempDir, stats, err
	}
	if err := d.config.Guard.Check(32 << 20); err != nil {
		return "", tempDir, stats, err
	}
	return root, tempDir, stats, nil
}
