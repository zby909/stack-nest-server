//go:build linux

package storage

import "syscall"

// ReadUsage 查询目录所在挂载点，使用当前用户可用块而非保留块。
func ReadUsage(path string) (Usage, error) {
	var info syscall.Statfs_t
	if err := syscall.Statfs(path, &info); err != nil {
		return Usage{}, err
	}
	return Usage{TotalBytes: info.Blocks * uint64(info.Bsize), FreeBytes: info.Bavail * uint64(info.Bsize)}, nil
}
