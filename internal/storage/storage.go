package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	MaxArchiveBytes  int64 = 90 << 20
	MaxExpandedBytes int64 = 200 << 20
	MaxFiles               = 5000
	MaxUsedPercent         = 90
	// UploadBudget 包含接收文件、归档副本、解压目录及文件系统开销。
	UploadBudget int64 = 2*MaxArchiveBytes + MaxExpandedBytes + 32<<20
)

var (
	ErrInsufficient = errors.New("insufficient storage")
	ErrUnavailable  = errors.New("storage information unavailable")
)

// Usage 描述数据目录所在文件系统对当前进程可用的空间。
type Usage struct {
	TotalBytes  uint64  `json:"totalBytes"`
	FreeBytes   uint64  `json:"freeBytes"`
	UsedPercent float64 `json:"usedPercent"`
}

// Guard 在实际写入目录上执行容量检查，Read 可由测试替换。
type Guard struct {
	Dir  string
	Read func(string) (Usage, error)
}

// New 创建使用本机文件系统信息的容量守卫。
func New(directory string) *Guard { return &Guard{Dir: directory, Read: ReadUsage} }

// Usage 获取磁盘状态；探测失败时禁止继续上传。
func (g *Guard) Usage() (Usage, error) {
	if err := os.MkdirAll(g.Dir, 0o755); err != nil {
		return Usage{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	value, err := g.Read(g.Dir)
	if err != nil || value.TotalBytes == 0 || value.FreeBytes > value.TotalBytes {
		return Usage{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	value.UsedPercent = float64(value.TotalBytes-value.FreeBytes) * 100 / float64(value.TotalBytes)
	return value, nil
}

// Check 同时拒绝已达到 90% 和预计写入后达到 90% 的情况。
func (g *Guard) Check(additionalBytes int64) error {
	value, err := g.Usage()
	if err != nil {
		return err
	}
	return CheckUsage(value, additionalBytes)
}

// CheckUsage 使用整数余量比较，保证阈值处也拒绝写入。
func CheckUsage(value Usage, additionalBytes int64) error {
	if value.TotalBytes == 0 || value.FreeBytes > value.TotalBytes || additionalBytes < 0 {
		return ErrUnavailable
	}
	reserve := value.TotalBytes / 10
	if value.TotalBytes%10 != 0 {
		reserve++
	}
	if value.FreeBytes <= reserve || uint64(additionalBytes) >= value.FreeBytes-reserve {
		return ErrInsufficient
	}
	return nil
}

// Copy 在有界缓冲区内复制，并定期复查磁盘、取消信号和后续阶段所需空间。
func Copy(ctx context.Context, guard *Guard, destination io.Writer, source io.Reader, reserve int64) (int64, error) {
	buffer := make([]byte, 64<<10)
	var written, checked int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		if guard != nil && written >= checked {
			if err := guard.Check(reserve + int64(len(buffer))); err != nil {
				return written, err
			}
			checked = written + 1<<20
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			m, writeErr := destination.Write(buffer[:n])
			written += int64(m)
			if writeErr != nil {
				return written, writeErr
			}
			if m != n {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}
