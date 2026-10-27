package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"stacknest/server/internal/activity"
	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

var errUploadTooLarge = errors.New("archive too large")

type receivedUpload struct {
	input     project.CreateInput
	file      *os.File
	directory string
}

// close 先关闭 Windows 文件句柄，再删除本次接收目录。
func (u *receivedUpload) close() {
	if u.file != nil {
		_ = u.file.Close()
	}
	_ = os.RemoveAll(u.directory)
}

// receiveUpload 流式接收归档到受容量检查保护的数据卷，避免系统临时盘被填满。
func (s *Server) receiveUpload(w http.ResponseWriter, r *http.Request, event *activity.Event, replacement bool) (_ *receivedUpload, resultErr error) {
	r.Body = http.MaxBytesReader(w, r.Body, s.config.MaxUploadSize+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, project.NewInvalidArchiveError("请使用包含 ZIP、RAR 或 HTML 的上传表单")
	}
	tempRoot := filepath.Join(s.config.Storage.Dir, "tmp")
	if err := os.MkdirAll(tempRoot, 0o755); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(tempRoot, "receive-*")
	if err != nil {
		return nil, err
	}
	upload := &receivedUpload{directory: directory}
	defer func() {
		if resultErr != nil {
			upload.close()
		}
	}()
	seen := make(map[string]bool)
	for count := 0; ; count++ {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, uploadReadError(err)
		}
		if count >= 4 {
			return nil, project.NewInvalidArchiveError("上传表单包含多余字段")
		}
		name := part.FormName()
		if seen[name] {
			return nil, project.NewInvalidArchiveError("上传表单包含重复字段")
		}
		seen[name] = true
		if name == "file" {
			fileName := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
			event.FileName = fileName
			if !project.SupportedUploadFile(fileName) {
				return nil, project.NewInvalidArchiveError("仅支持 ZIP、RAR 或单个 HTML 静态项目")
			}
			upload.file, err = os.Create(filepath.Join(directory, "archive"))
			if err != nil {
				return nil, err
			}
			written, err := storage.Copy(r.Context(), s.config.Storage, upload.file, io.LimitReader(part, s.config.MaxUploadSize+1), storage.MaxArchiveBytes+storage.MaxExpandedBytes+(32<<20))
			event.ArchiveBytes = written
			if err != nil {
				return nil, err
			}
			if written > s.config.MaxUploadSize {
				return nil, errUploadTooLarge
			}
			if _, err := upload.file.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			upload.input.Source = upload.file
			upload.input.FileName = fileName
		} else {
			if replacement {
				return nil, project.NewInvalidArchiveError("更新项目只接受 file 字段")
			}
			limits := map[string]int64{"name": 256, "slug": 52, "description": 4096}
			limit, allowed := limits[name]
			if !allowed || part.FileName() != "" {
				return nil, project.NewInvalidArchiveError("上传表单字段无效")
			}
			value, err := io.ReadAll(io.LimitReader(part, limit+1))
			if err != nil {
				return nil, uploadReadError(err)
			}
			if int64(len(value)) > limit {
				return nil, project.NewInvalidArchiveError(fmt.Sprintf("%s 字段超过长度限制", name))
			}
			switch name {
			case "name":
				upload.input.Name = string(value)
			case "slug":
				upload.input.Slug = string(value)
				event.Slug = string(value)
			case "description":
				upload.input.Description = string(value)
			}
		}
		if err := part.Close(); err != nil {
			return nil, uploadReadError(err)
		}
	}
	if upload.file == nil {
		return nil, project.NewInvalidArchiveError("请选择 ZIP、RAR 或 HTML 文件")
	}
	return upload, nil
}

// uploadReadError 将不完整表单归类为客户端输入错误，同时保留容量和取消信号。
func uploadReadError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return project.NewInvalidArchiveError("上传内容不完整或表单格式无效，请重新上传")
}
