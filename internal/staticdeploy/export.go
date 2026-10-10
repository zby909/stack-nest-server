package staticdeploy

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

type archiveFile struct {
	*os.File
	size int64
}

// Size 返回已完成的 ZIP 大小，供下载接口设置长度。
func (a *archiveFile) Size() int64 { return a.size }

// Close 在连接结束后删除私有快照，避免导出文件长期占用磁盘。
func (a *archiveFile) Close() error {
	closeErr := a.File.Close()
	removeErr := os.Remove(a.Name())
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

type archiveWriter struct {
	deployer                *Deployer
	ctx                     context.Context
	file                    *os.File
	written, limit, checked int64
}

// Write 同时限制 ZIP 实际大小、磁盘余量和请求生命周期。
func (w *archiveWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(data)) > w.limit-w.written {
		return 0, project.ErrExportUnavailable
	}
	if w.written >= w.checked {
		if err := w.deployer.config.Guard.Check(1 << 20); err != nil {
			return 0, err
		}
		w.checked = w.written + (1 << 20)
	}
	n, err := w.file.Write(data)
	w.written += int64(n)
	return n, err
}

// Export 打包受限目录中的普通文件，不包含归档、日志、密钥或其他项目。
func (d *Deployer) Export(ctx context.Context, slug string, previous bool) (project.Archive, error) {
	slot := "sites"
	if previous {
		slot = "previous"
	}
	return d.ExportAt(ctx, slug, slot)
}

// ExportAt 对草稿、当前版和上一版使用一致的安全校验。
func (d *Deployer) ExportAt(ctx context.Context, slug, slot string) (project.Archive, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := d.ensureSettled(slug); err != nil {
		return nil, err
	}
	directory, err := d.versionDirectory(slug, slot)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, project.ErrExportUnavailable
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	index, err := root.Lstat("index.html")
	if err != nil || !index.Mode().IsRegular() {
		return nil, project.ErrExportUnavailable
	}
	// 预留整个 ZIP 的最坏体积，压缩过程中仍持续检查 90% 磁盘阈值。
	limit := d.config.MaxBytes + (16 << 20)
	if err := d.config.Guard.Check(limit); err != nil {
		return nil, err
	}
	tempDir := filepath.Join(d.config.DataDir, "exports")
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(tempDir, "snapshot-*.zip")
	if err != nil {
		return nil, err
	}
	archive := &archiveFile{File: file}
	complete := false
	defer func() {
		if !complete {
			_ = archive.Close()
		}
	}()
	output := &archiveWriter{deployer: d, ctx: ctx, file: file, limit: limit}
	writer := zip.NewWriter(output)
	var bytes int64
	var count int
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
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
		if !info.Mode().IsRegular() || strings.Contains(name, "\\") || len(name) > 1024 {
			return project.ErrExportUnavailable
		}
		count++
		if count > d.config.MaxFiles || info.Size() > d.config.MaxBytes-bytes {
			return project.ErrExportUnavailable
		}
		input, err := root.Open(name)
		if err != nil {
			return err
		}
		defer input.Close()
		opened, err := input.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			return project.ErrExportUnavailable
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name, header.Method = name, zip.Deflate
		destination, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		copied, err := storage.Copy(ctx, nil, destination, io.LimitReader(input, info.Size()+1), 0)
		bytes += copied
		if err != nil {
			return err
		}
		if copied != info.Size() {
			return project.ErrExportUnavailable
		}
		return nil
	})
	closeErr := writer.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	archive.size, complete = output.written, true
	return archive, nil
}

// RecoverExports 清理上次进程退出遗留的 ZIP；这些文件从未对静态路由公开。
func (d *Deployer) RecoverExports() error {
	directory := filepath.Join(d.config.DataDir, "exports")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "snapshot-") && strings.HasSuffix(entry.Name(), ".zip") {
			if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
