package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/backup"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

// EditProject 严格读取展示字段与页面版本，不接受 slug、归属或部署信息。
func (s *Server) EditProject(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug, params generated.EditProjectParams) {
	event := newActivity(r, "edit")
	event.Slug = slug
	defer s.finishActivity(r, event)
	value, err := s.projects.Get(r.Context(), slug)
	if err == nil && !canManageProject(s.requestUser(r), value) {
		err = project.ErrNotFound
	}
	if err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	event.ProjectOwnerID = value.OwnerID
	match, err := strconv.Unquote(params.IfMatch)
	expectedAt, parseErr := time.Parse(time.RFC3339Nano, match)
	if err != nil || parseErr != nil || !strings.HasPrefix(params.IfMatch, "\"") {
		event.Message = "If-Match 必须是带双引号的项目更新时间"
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: event.Message})
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		event.Message = "请发送 JSON 请求"
		writeJSON(w, http.StatusUnsupportedMediaType, generated.ErrorResponse{Error: event.Message})
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	// 合法字段最多 4352 字节，JSON 转义最多扩展六倍；仍限制整个请求体。
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	var extra any
	if err := decoder.Decode(&body); err != nil {
		event.Message = "请求内容无效"
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: event.Message})
		return
	}
	if err := decoder.Decode(&extra); err != io.EOF || body.Name == nil || body.Description == nil {
		event.Message = "请求内容无效"
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: event.Message})
		return
	}
	user := s.requestUser(r)
	updated, err := s.projects.EditAs(r.Context(), slug, project.Actor{ID: user.ID, IsAdmin: user.Role == auth.RoleAdmin}, project.MetadataInput{Name: *body.Name, Description: *body.Description, ExpectedAt: expectedAt})
	if err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	event.Status, event.Message = "succeeded", "项目名称和说明已保存"
	writeJSON(w, http.StatusOK, s.toProjectResponse(r, updated))
}

// ExportProject 每次只保留一个私有 ZIP 快照，限时传输并在连接结束后清理。
func (s *Server) ExportProject(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug, params generated.ExportProjectParams) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	r = r.WithContext(ctx)
	event := newActivity(r, "export")
	event.Slug = slug
	defer s.finishActivity(r, event)
	user := s.requestUser(r)
	value, err := s.projects.Get(r.Context(), slug)
	if err == nil && !canManageProject(user, value) {
		err = project.ErrNotFound
	}
	if err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	event.ProjectOwnerID = value.OwnerID
	if !s.exportMu.TryLock() {
		event.Message = "另一个导出正在处理中，请稍后重试"
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusTooManyRequests, generated.ErrorResponse{Error: event.Message})
		return
	}
	defer s.exportMu.Unlock()
	result, err := s.projects.ExportAs(r.Context(), slug, project.Actor{ID: user.ID, IsAdmin: user.Role == auth.RoleAdmin}, params.DeploymentId)
	if err != nil {
		if errors.Is(err, storage.ErrInsufficient) || errors.Is(err, storage.ErrUnavailable) {
			code := http.StatusInsufficientStorage
			event.Message = "服务器空间不足，暂时无法生成导出文件，请联系管理员清理后重试"
			if errors.Is(err, storage.ErrUnavailable) {
				code, event.Message = http.StatusServiceUnavailable, "暂时无法检查导出空间，请稍后重试"
			}
			writeJSON(w, code, generated.ErrorResponse{Error: event.Message})
		} else {
			event.Message = s.writeProjectError(w, r, err)
		}
		return
	}
	defer func() {
		if err := result.Archive.Close(); err != nil {
			slog.ErrorContext(r.Context(), "clean exported archive", "request_id", requestID(r), "error", err)
		}
	}()
	fileName := slug + "-" + result.Deployment.ID + ".zip"
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(fileName))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("ETag", strconv.Quote(result.Deployment.ID))
	deadline, _ := ctx.Deadline()
	_ = http.NewResponseController(w).SetWriteDeadline(deadline)
	defer http.NewResponseController(w).SetWriteDeadline(time.Time{})
	response := &exportResponse{ResponseWriter: w}
	http.ServeContent(response, r, fileName, result.Deployment.CreatedAt, result.Archive)
	event.FileName, event.ArchiveBytes = fileName, result.Archive.Size()
	event.Status, event.Message = "succeeded", "部署 ZIP 已生成并发送"
	if response.code >= 400 {
		event.Status, event.Message = "failed", "导出请求的下载范围或缓存条件无效"
	} else if r.Context().Err() != nil || response.err != nil {
		event.Status, event.Message = "failed", "导出传输已中断"
	}
}

type exportResponse struct {
	http.ResponseWriter
	err  error
	code int
}

// WriteHeader 保留下载结果，范围请求错误不能被记成成功导出。
func (w *exportResponse) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write 记录客户端下载中断，不将不完整传输记为成功。
func (w *exportResponse) Write(data []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	if err != nil {
		w.err = err
	}
	return n, err
}

// GetBackupStatus 仅向管理员返回独立目录中的安全摘要。
func (s *Server) GetBackupStatus(w http.ResponseWriter, r *http.Request) {
	if s.requestUser(r).Role != auth.RoleAdmin {
		writeJSON(w, http.StatusForbidden, generated.ErrorResponse{Error: "仅管理员可访问"})
		return
	}
	status, err := backup.Read(s.config.BackupStatusFile, time.Now().UTC())
	if err != nil {
		slog.ErrorContext(r.Context(), "read backup status", "request_id", requestID(r), "error", err)
		writeJSON(w, http.StatusServiceUnavailable, generated.ErrorResponse{Error: "备份状态暂时无法读取，请检查备份任务"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}
