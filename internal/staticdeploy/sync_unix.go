//go:build !windows

package staticdeploy

import "os"

// syncDirectory 在生产环境同步目录项，确保重命名顺序可恢复。
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
