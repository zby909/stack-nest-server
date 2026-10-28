package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
	"stacknest/server/internal/resumable"
	"stacknest/server/internal/storage"
)

// uploadStatusResponse 只返回浏览器恢复所需信息，不公开内部目录和其他用户身份。
type uploadStatusResponse struct {
	ID           string    `json:"id"`
	Offset       int64     `json:"offset"`
	Size         int64     `json:"size"`
	ChunkSize    int64     `json:"chunkSize"`
	MinChunkSize int64     `json:"minChunkSize"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Completed    bool      `json:"completed"`
}

// writeUploadStatus 返回不可缓存的已提交分片偏移。
func writeUploadStatus(w http.ResponseWriter, code int, value resumable.Ticket) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, code, uploadStatusResponse{ID: value.Request.ID, Offset: value.Offset, Size: value.Request.Size, ChunkSize: resumable.ChunkSize, MinChunkSize: resumable.MinChunkSize, ExpiresAt: value.ExpiresAt, Completed: value.ResultID != ""})
}

// CreateUpload 创建或恢复当前用户的上传会话，在接收文件前校验目标权限和版本。
func (s *Server) CreateUpload(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeJSON(w, 415, generated.ErrorResponse{Error: "请发送 JSON 请求"})
		return
	}
	var input resumable.Request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, resumable.MetadataJSONLimit))
	decoder.DisallowUnknownFields()
	var extra any
	if err := decoder.Decode(&input); err != nil {
		s.writeUploadError(w, r, resumable.ErrInvalid)
		return
	}
	if err := decoder.Decode(&extra); err != io.EOF || s.uploads.Validate(input) != nil {
		s.writeUploadError(w, r, resumable.ErrInvalid)
		return
	}
	user := s.requestUser(r)
	if grant, ok := deploymentGrant(r); ok && (input.Kind != "update" || input.Slug != grant.Slug || !input.Preview) {
		writeJSON(w, 403, generated.ErrorResponse{Error: "部署 Token 只能为绑定的项目上传预览"})
		return
	}
	if _, err := s.uploads.Get(input.ID, s.uploadOwner(r)); errors.Is(err, resumable.ErrNotFound) {
		target := input.Slug
		if target == "" {
			target = project.Slugify(input.Name)
		}
		if target == "" {
			target = "project-" + input.ID
		}
		value, err := s.projects.Get(r.Context(), target)
		if grant, ok := deploymentGrant(r); ok && err == nil && (value.ID != grant.ProjectID || value.OwnerID != grant.OwnerID) {
			err = project.ErrNotFound
		}
		if input.Kind == "update" {
			if err == nil && !canManageProject(user, value) {
				err = project.ErrNotFound
			}
			if err == nil && (project.CurrentID(value) != input.ExpectedID) {
				err = project.ErrVersionConflict
			}
			if err == nil && value.Draft != nil {
				err = project.ErrDraftExists
			}
		} else {
			if err == nil {
				err = project.ErrAlreadyExists
			} else if errors.Is(err, project.ErrNotFound) {
				err = nil
			}
		}
		if err != nil {
			s.writeProjectError(w, r, err)
			return
		}
	}
	ticket, err := s.uploads.Create(s.uploadOwner(r), input)
	if err != nil {
		s.writeUploadError(w, r, err)
		return
	}
	writeUploadStatus(w, http.StatusCreated, ticket)
}

// GetUpload 每次恢复先查询服务端偏移，客户端不能自行宣称已上传字节。
func (s *Server) GetUpload(w http.ResponseWriter, r *http.Request, id string) {
	ticket, err := s.uploads.Get(id, s.uploadOwner(r))
	if err != nil {
		s.writeUploadError(w, r, err)
		return
	}
	writeUploadStatus(w, http.StatusOK, ticket)
}

// AppendUpload 有界接收一个分片，完整收到后才推进进度，限制并发缓冲区占用。
func (s *Server) AppendUpload(w http.ResponseWriter, r *http.Request, id string, params generated.AppendUploadParams) {
	if _, err := s.uploads.Get(id, s.uploadOwner(r)); err != nil {
		s.writeUploadError(w, r, err)
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		writeJSON(w, 415, generated.ErrorResponse{Error: "请发送二进制分片"})
		return
	}
	if !s.chunkMu.TryLock() {
		w.Header().Set("Retry-After", "2")
		writeJSON(w, 429, generated.ErrorResponse{Error: "另一个分片正在传输，请稍后继续"})
		return
	}
	defer s.chunkMu.Unlock()
	if err := s.config.Storage.Check(storage.UploadBudget); err != nil {
		s.writeUploadError(w, r, err)
		return
	}
	controller := http.NewResponseController(w)
	// 缩短单片占用接收锁的时间；弱网客户端可缩小分片后继续。
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, resumable.ChunkSize))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, generated.ErrorResponse{Error: "单个分片超过大小上限，请刷新页面后继续"})
			return
		}
		slog.Warn("upload chunk interrupted", "request_id", requestID(r), "received_bytes", len(data), "expected_bytes", r.ContentLength)
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusRequestTimeout, generated.ErrorResponse{Error: "分片传输中断或超时，已确认的上传进度仍保留"})
		return
	}
	ticket, err := s.uploads.Append(r.Context(), id, s.uploadOwner(r), params.UploadOffset, data)
	if err != nil {
		s.writeUploadError(w, r, err)
		return
	}
	writeUploadStatus(w, http.StatusOK, ticket)
}

// DeleteUpload 放弃临时上传，完成过的发布不受影响。
func (s *Server) DeleteUpload(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.uploads.Delete(id, s.uploadOwner(r)); err != nil && !errors.Is(err, resumable.ErrNotFound) {
		s.writeUploadError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CompleteUpload 使用固定部署编号确认发布结果，连接断开后仍完成已开始的事务。
func (s *Server) CompleteUpload(w http.ResponseWriter, r *http.Request, id string) {
	user := s.requestUser(r)
	if _, err := s.uploads.Get(id, s.uploadOwner(r)); err != nil {
		s.writeUploadError(w, r, err)
		return
	}
	if !s.uploadMu.TryLock() {
		w.Header().Set("Retry-After", "5")
		writeJSON(w, 429, generated.ErrorResponse{Error: "另一个发布正在处理中，请稍后确认结果"})
		return
	}
	defer s.uploadMu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	ticket, err := s.uploads.Publish(ctx, id, s.uploadOwner(r), func(ticket resumable.Ticket, source io.Reader) (string, error) {
		input := ticket.Request
		action := "upload"
		if input.Kind == "update" {
			action = "update"
		}
		if input.Preview {
			action = "draft"
		}
		event := newActivity(r, action)
		event.Slug, event.FileName, event.ArchiveBytes = ticket.TargetSlug, input.FileName, input.Size
		defer s.finishActivity(r.WithContext(ctx), event)
		var result project.CreatedProject
		var err error
		if input.Kind == "update" {
			result, err = s.projects.UpdateAs(ctx, ticket.TargetSlug, s.deploymentActor(r), project.UpdateInput{Preview: input.Preview, Notes: input.Notes, DeploymentID: "dep-" + id, ExpectedID: input.ExpectedID, FileName: input.FileName, Source: source})
		} else {
			result, err = s.projects.Create(ctx, project.CreateInput{Preview: input.Preview, Notes: input.Notes, DeploymentID: "dep-" + id, OwnerID: user.ID, OwnerEmail: user.Email, Name: input.Name, Description: input.Description, Slug: ticket.TargetSlug, FileName: input.FileName, Source: source})
		}
		if err != nil {
			event.Message = "续传文件发布失败，请根据请求编号检查"
			var invalid *project.InvalidArchiveError
			switch {
			case errors.As(err, &invalid):
				event.Message = invalid.Reason
			case errors.Is(err, project.ErrVersionConflict):
				event.Message = "项目版本已发生变化，请刷新项目列表后重试"
			case errors.Is(err, storage.ErrInsufficient):
				event.Message = uploadStorageReason(user, false)
			case errors.Is(err, storage.ErrUnavailable):
				event.Message = uploadStorageReason(user, true)
			case errors.Is(err, project.ErrNotFound):
				event.Message = "项目不存在或已失去访问权限"
			}
			return "", err
		}
		event.Status, event.Message = "succeeded", "续传文件发布成功"
		if input.Preview {
			event.Message = "待发布版本已创建，预览确认后可正式发布"
		}
		event.ProjectOwnerID, event.ExpandedBytes, event.FileCount = result.Project.OwnerID, result.Files.Bytes, result.Files.Count
		return result.Project.ID, nil
	})
	if err != nil {
		s.writeUploadError(w, r, err)
		return
	}
	value, err := s.projects.Get(ctx, ticket.TargetSlug)
	if grant, ok := deploymentGrant(r); ok && err == nil && (value.ID != grant.ProjectID || value.OwnerID != grant.OwnerID) {
		err = project.ErrNotFound
	}
	if err == nil && (value.ID != ticket.ResultID || !canManageProject(user, value)) {
		err = project.ErrNotFound
	}
	if err != nil {
		s.writeProjectError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toProjectResponse(r.WithContext(ctx), value))
}

// writeUploadError 将续传错误映射为可操作反馈，其他项目错误沿用统一处理。
func (s *Server) writeUploadError(w http.ResponseWriter, r *http.Request, err error) {
	code, message := 0, ""
	switch {
	case errors.Is(err, resumable.ErrNotFound):
		code, message = 404, "上传记录不存在或已过期，请重新选择文件上传"
	case errors.Is(err, resumable.ErrConflict):
		code, message = 409, "上传进度或参数已变化，请查询进度后继续"
	case errors.Is(err, resumable.ErrLimit):
		code, message = 429, "未完成上传数量已达上限，请先放弃其他上传或等待过期清理"
	case errors.Is(err, resumable.ErrInvalid):
		code, message = 400, "上传参数或分片无效，请检查文件大小、格式和完整性"
	case errors.Is(err, resumable.ErrChecksum):
		code, message = 422, "文件校验失败，请放弃本次上传后重新选择原文件"
	default:
		s.writeProjectError(w, r, err)
		return
	}
	writeJSON(w, code, generated.ErrorResponse{Error: message})
}
