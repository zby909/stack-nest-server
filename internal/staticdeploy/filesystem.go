package staticdeploy

import (
	"context"
	"os"
	"path/filepath"
	"stacknest/server/internal/project"
)

// measureSite 统计解压后的实际文件，不包含原始压缩包。
func measureSite(ctx context.Context, root string) (project.PublishedFiles, error) {
	var result project.PublishedFiles
	err := filepath.Walk(root, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			result.Bytes += info.Size()
			result.Count++
		}
		return nil
	})
	return result, err
}
