//go:build !linux && !windows

package storage

// ReadUsage 在尚未实现容量检测的平台上拒绝上传，避免跳过保护。
func ReadUsage(string) (Usage, error) { return Usage{}, ErrUnavailable }
