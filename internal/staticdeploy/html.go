package staticdeploy

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

// prepareHTML 原样保存单个 HTML 为固定入口，不改写内容、编码或文件中的资源链接。
func prepareHTML(ctx context.Context, guard *storage.Guard, sourcePath, destination string, maxFiles int, maxBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return project.NewInvalidArchiveError("HTML 文件不能为空且必须是普通文件")
	}
	if maxFiles < 1 || info.Size() > maxBytes {
		return project.NewInvalidArchiveError("HTML 文件超过发布大小限制")
	}
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	output, err := os.OpenFile(filepath.Join(destination, "index.html"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	written, copyErr := storage.Copy(ctx, guard, output, io.LimitReader(source, maxBytes+1), 0)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxBytes {
		return project.NewInvalidArchiveError("HTML 文件超过发布大小限制")
	}
	return nil
}
