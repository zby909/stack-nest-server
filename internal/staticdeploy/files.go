package staticdeploy

import (
	"context"
	"io/fs"
	"os"
	"strings"

	"stacknest/server/internal/project"
)

// Files 列出受限根目录下的普通文件；不接受客户端路径、不跟随链接、不读取文件内容。
func (d *Deployer) Files(ctx context.Context, slug string, previous bool) ([]project.PublishedFile, error) {
	slot := "sites"
	if previous {
		slot = "previous"
	}
	return d.FilesAt(ctx, slug, slot)
}

// FilesAt 对草稿、当前版和上一版使用一致的安全校验。
func (d *Deployer) FilesAt(ctx context.Context, slug, slot string) ([]project.PublishedFile, error) {
	if err := d.ensureSettled(slug); err != nil {
		return nil, err
	}
	directory, err := d.versionDirectory(slug, slot)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, project.ErrUsageUnavailable
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	index, err := root.Lstat("index.html")
	if err != nil || !index.Mode().IsRegular() {
		return nil, project.ErrUsageUnavailable
	}
	files := make([]project.PublishedFile, 0)
	var total int64
	entries := 0
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > d.config.MaxFiles*2+1 || len(name) > 1024 || strings.Contains(name, "\\") {
			return project.ErrUsageUnavailable
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || len(files) >= d.config.MaxFiles || info.Size() > d.config.MaxBytes-total {
			return project.ErrUsageUnavailable
		}
		total += info.Size()
		files = append(files, project.PublishedFile{Path: name, Bytes: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
