package staticdeploy

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"stacknest/server/internal/storage"
	"strings"

	"stacknest/server/internal/project"
)

// extractArchive 按扩展名准备站点；ZIP/RAR 校验归档格式，HTML 原样保存为入口。
func extractArchive(archivePath, fileName, destination string, maxFiles int, maxBytes int64) error {
	return extractArchiveGuarded(context.Background(), nil, archivePath, fileName, destination, maxFiles, maxBytes)
}

// extractArchiveGuarded 在解压或复制过程中持续检查容量与请求取消。
func extractArchiveGuarded(ctx context.Context, guard *storage.Guard, archivePath, fileName, destination string, maxFiles int, maxBytes int64) error {
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".zip":
		return extractZipGuarded(ctx, guard, archivePath, destination, maxFiles, maxBytes)
	case ".rar":
		return extractRARGuarded(ctx, guard, archivePath, destination, maxFiles, maxBytes)
	case ".html", ".htm":
		return prepareHTML(ctx, guard, archivePath, destination, maxFiles, maxBytes)
	default:
		return project.NewInvalidArchiveError("仅支持 ZIP、RAR 或单个 HTML 静态项目")
	}
}

// extractZip 保留不依赖系统磁盘的归档边界测试入口。
func extractZip(zipPath, destination string, maxFiles int, maxBytes int64) error {
	return extractZipGuarded(context.Background(), nil, zipPath, destination, maxFiles, maxBytes)
}

// extractZipGuarded 同时校验 ZIP 声明大小和实际输出字节数。
func extractZipGuarded(ctx context.Context, guard *storage.Guard, zipPath, destination string, maxFiles int, maxBytes int64) error {
	file, err := os.Open(zipPath)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	reader, err := newZIPReader(ctx, file, info.Size())
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if errors.Is(err, errZIPIndexTooLarge) {
			return project.NewInvalidArchiveError("ZIP 目录信息过大，请减少条目或移除归档注释后重试")
		}
		return project.NewInvalidArchiveError("ZIP 文件损坏或格式无效")
	}

	if len(reader.File) > maxFiles {
		return project.NewInvalidArchiveError("ZIP 文件数量超过 5000 个")
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}

	var total int64
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := maxBytes - total
		if remaining < 0 || file.UncompressedSize64 > uint64(remaining) {
			return project.NewInvalidArchiveError("ZIP 解压后大小不能超过 200 MiB")
		}

		target, err := archiveTarget(destination, file.Name, "ZIP")
		if err != nil {
			return err
		}
		info := file.FileInfo()
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return project.NewInvalidArchiveError("ZIP 中不允许包含链接或特殊文件")
		}
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		source, err := file.Open()
		if err != nil {
			return project.NewInvalidArchiveError("ZIP 中包含无法读取的文件")
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			source.Close()
			return err
		}

		written, copyErr := storage.Copy(ctx, guard, out, io.LimitReader(source, maxBytes-total+1), 0)
		total += written
		closeErr := out.Close()
		source.Close()

		if copyErr != nil {
			if errors.Is(copyErr, zip.ErrChecksum) || errors.Is(copyErr, zip.ErrFormat) || errors.Is(copyErr, zip.ErrAlgorithm) {
				return project.NewInvalidArchiveError("ZIP 文件损坏或包含不支持的压缩格式")
			}
			return copyErr
		}
		if total > maxBytes {
			return project.NewInvalidArchiveError("ZIP 解压后大小不能超过 200 MiB")
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// archiveTarget 将归档内路径限定在解压目录，统一处理 Windows 与 Unix 分隔符。
func archiveTarget(destination, name, format string) (string, error) {
	invalid := project.NewInvalidArchiveError(format + " 中包含非法路径")
	normalized := strings.ReplaceAll(name, `\`, "/")
	if normalized == "" || strings.HasPrefix(normalized, "/") || strings.ContainsAny(normalized, ":\x00") {
		return "", invalid
	}
	for _, part := range strings.Split(normalized, "/") {
		if part == ".." {
			return "", invalid
		}
	}
	relative := filepath.FromSlash(normalized)
	if !filepath.IsLocal(relative) {
		return "", invalid
	}
	target := filepath.Join(destination, relative)
	rel, err := filepath.Rel(destination, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", invalid
	}
	return target, nil
}

func detectSiteRoot(directory string) (string, error) {
	if isRegularFile(filepath.Join(directory, "index.html")) {
		return directory, nil
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}

	directories := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			directories = append(directories, entry)
		}
	}
	if len(directories) == 1 {
		nested := filepath.Join(directory, directories[0].Name())
		if isRegularFile(filepath.Join(nested, "index.html")) {
			return nested, nil
		}
	}
	return "", project.NewInvalidArchiveError("压缩包根目录或唯一一级目录中必须包含 index.html 文件")
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
