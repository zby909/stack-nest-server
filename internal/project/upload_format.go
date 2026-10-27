package project

import (
	"path/filepath"
	"strings"
)

// SupportedUploadFile 统一限制完整上传与断点续传接受的静态项目文件类型。
func SupportedUploadFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".zip", ".rar", ".html", ".htm":
		return true
	default:
		return false
	}
}
