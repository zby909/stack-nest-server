package staticdeploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

// Config 定义静态站点发布限制。
type Config struct {
	DataDir  string
	MaxFiles int
	MaxBytes int64
	Guard    *storage.Guard
}

// Deployer 负责静态项目的临时文件、解压和最终目录发布。
type Deployer struct {
	config Config
}

// New 创建静态站点发布器。
func New(config Config) *Deployer {
	if config.Guard == nil {
		config.Guard = storage.New(config.DataDir)
	}
	return &Deployer{config: config}
}

// Publish 校验归档，记录发布标记，再通过同卷重命名公开完整目录。
func (d *Deployer) Publish(ctx context.Context, slug, fileName string, source io.Reader) (project.PublishedFiles, error) {
	empty := project.PublishedFiles{}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if !project.ValidSlug(slug) {
		return empty, project.ErrInvalidSlug
	}
	for _, directory := range []string{"trash", "sites", "previous", "changes"} {
		if _, err := os.Lstat(filepath.Join(d.config.DataDir, directory, slug)); err == nil {
			return empty, project.ErrAlreadyExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return empty, err
		}
	}
	if err := d.config.Guard.Check(storage.MaxArchiveBytes + d.config.MaxBytes + (32 << 20)); err != nil {
		return empty, err
	}
	if err := d.beginPublication(slug); err != nil {
		return empty, err
	}
	published := false
	defer func() {
		if !published {
			_ = d.CompletePublication(slug)
		}
	}()
	root, tempDir, stats, err := d.prepareSite(ctx, fileName, source)
	if err != nil {
		return empty, err
	}
	defer os.RemoveAll(tempDir)
	sitesDir := filepath.Join(d.config.DataDir, "sites")
	if err := os.MkdirAll(sitesDir, 0o755); err != nil {
		return empty, err
	}
	stageDir := filepath.Join(sitesDir, ".publish-"+slug+"-"+uuid.NewString())
	// 临时目录和发布目录在同一数据卷，重命名避免再复制一份解压内容。
	if err := os.Rename(root, stageDir); err != nil {
		return empty, err
	}
	defer os.RemoveAll(stageDir)
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := d.config.Guard.Check(0); err != nil {
		return empty, err
	}
	target := filepath.Join(sitesDir, slug)
	if _, err := os.Lstat(target); err == nil {
		return empty, project.ErrAlreadyExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return empty, err
	}
	if err := d.SetPaused(slug, false); err != nil {
		return empty, err
	}
	if err := os.Rename(stageDir, target); err != nil {
		return empty, fmt.Errorf("发布失败: %w", err)
	}
	published = true
	return stats, nil
}

// saveArchive 有界保存压缩包，并在磁盘占用变化或请求取消时停止。
func (d *Deployer) saveArchive(ctx context.Context, source io.Reader, path string) error {
	output, err := os.Create(path)
	if err != nil {
		return err
	}
	written, copyErr := storage.Copy(ctx, d.config.Guard, output, io.LimitReader(source, storage.MaxArchiveBytes+1), d.config.MaxBytes+(32<<20))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > storage.MaxArchiveBytes {
		return project.NewInvalidArchiveError("压缩包不能超过 90 MiB")
	}
	return nil
}

// Delete 删除一个静态项目的最终发布目录。
func (d *Deployer) Delete(ctx context.Context, slug string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(d.config.DataDir, "sites", slug))
}
