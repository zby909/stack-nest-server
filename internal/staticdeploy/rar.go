package staticdeploy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/nwaples/rardecode/v2"

	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

const maxRARDictionarySize = 64 << 20

// extractRAR 逐项解压单卷 RAR，并限制链接、文件数量、展开大小和解码内存。
func extractRAR(archivePath, destination string, maxFiles int, maxBytes int64) error {
	return extractRARGuarded(context.Background(), nil, archivePath, destination, maxFiles, maxBytes)
}

// extractRARGuarded 在解码过程中执行容量与取消检查。
func extractRARGuarded(ctx context.Context, guard *storage.Guard, archivePath, destination string, maxFiles int, maxBytes int64) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()

	reader, err := rardecode.NewReader(archive, rardecode.MaxDictionarySize(maxRARDictionarySize))
	if err != nil {
		return invalidRARError(err)
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}

	var total int64
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return invalidRARError(err)
		}
		if count >= maxFiles {
			return project.NewInvalidArchiveError("RAR 文件数量超过 5000 个")
		}
		if err := validateRARHeader(header); err != nil {
			return err
		}

		remaining := maxBytes - total
		if remaining < 0 || header.UnPackedSize < 0 || (!header.UnKnownSize && header.UnPackedSize > remaining) {
			return project.NewInvalidArchiveError("RAR 解压后大小不能超过 200 MiB")
		}
		target, err := archiveTarget(destination, header.Name, "RAR")
		if err != nil {
			return err
		}
		if header.IsDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		written, copyErr := storage.Copy(ctx, guard, out, io.LimitReader(reader, remaining+1), 0)
		total += written
		closeErr := out.Close()
		if copyErr != nil {
			if errors.Is(copyErr, storage.ErrInsufficient) || errors.Is(copyErr, storage.ErrUnavailable) || errors.Is(copyErr, context.Canceled) || errors.Is(copyErr, context.DeadlineExceeded) {
				return copyErr
			}
			var pathErr *os.PathError
			if errors.As(copyErr, &pathErr) {
				return copyErr
			}
			return invalidRARError(copyErr)
		}
		if total > maxBytes {
			return project.NewInvalidArchiveError("RAR 解压后大小不能超过 200 MiB")
		}
		if closeErr != nil {
			return closeErr
		}
	}
}

// validateRARHeader 拒绝加密条目、重定向链接和非普通文件。
func validateRARHeader(header *rardecode.FileHeader) error {
	if header.Encrypted || header.HeaderEncrypted {
		return project.NewInvalidArchiveError("不支持加密 RAR 文件")
	}
	mode := header.Mode()
	if header.LinkType != rardecode.LinkTypeNone || mode&os.ModeSymlink != 0 {
		return project.NewInvalidArchiveError("RAR 中不允许包含链接")
	}
	if header.IsDir && (header.UnKnownSize || header.UnPackedSize != 0) {
		return project.NewInvalidArchiveError("RAR 目录条目不能包含文件内容")
	}
	if header.IsDir && !mode.IsDir() {
		return project.NewInvalidArchiveError("RAR 中不允许包含特殊文件")
	}
	if !header.IsDir && !mode.IsRegular() {
		return project.NewInvalidArchiveError("RAR 中不允许包含特殊文件")
	}
	return nil
}

// invalidRARError 将解码失败转换为可安全展示的上传错误。
func invalidRARError(err error) error {
	switch {
	case errors.Is(err, rardecode.ErrArchiveEncrypted), errors.Is(err, rardecode.ErrArchivedFileEncrypted), errors.Is(err, rardecode.ErrBadPassword):
		return project.NewInvalidArchiveError("不支持加密 RAR 文件")
	case errors.Is(err, rardecode.ErrMultiVolume):
		return project.NewInvalidArchiveError("不支持分卷 RAR，请上传单个完整 RAR 文件")
	case errors.Is(err, rardecode.ErrDictionaryTooLarge):
		return project.NewInvalidArchiveError("RAR 解码字典不能超过 64 MiB")
	default:
		return project.NewInvalidArchiveError("RAR 文件损坏或包含不支持的压缩格式")
	}
}
