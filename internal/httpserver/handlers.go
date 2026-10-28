package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
	openapispec "stacknest/server/openapi"
)

// GetOpenAPISpec 返回嵌入二进制的 OpenAPI 3.1 契约。
func (s *Server) GetOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapispec.Document)
}

// ListProjects 返回当前用户可管理的项目；管理员可查看全部。
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	values, err := s.projects.List(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "list projects", "error", err)
		writeJSON(w, http.StatusInternalServerError, generated.ErrorResponse{Error: "项目列表加载失败"})
		return
	}

	response := generated.ProjectListResponse{
		Projects: make([]generated.Project, 0, len(values)),
	}
	for _, value := range values {
		if !auth.IsAdmin(user.Role) && value.OwnerID != user.ID {
			continue
		}
		response.Projects = append(response.Projects, s.toProjectResponse(r, value))
	}
	writeJSON(w, http.StatusOK, response)
}

// CreateProject 检查磁盘后接收 HTML 或压缩包，记录成功、失败及实际发布体积。
func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request) {
	event := newActivity(r, "upload")
	defer s.finishActivity(r, event)
	if !s.uploadMu.TryLock() {
		event.Message = "另一个上传正在处理中，请稍后重试"
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusTooManyRequests, generated.ErrorResponse{Error: event.Message})
		return
	}
	defer s.uploadMu.Unlock()
	if err := s.config.Storage.Check(storage.UploadBudget); err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	if r.ContentLength > s.config.MaxUploadSize+(1<<20) {
		event.Message = s.writeProjectError(w, r, errUploadTooLarge)
		return
	}
	upload, err := s.receiveUpload(w, r, event, false)
	if err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	defer upload.close()
	user := s.requestUser(r)
	upload.input.OwnerID = user.ID
	upload.input.OwnerEmail = user.Email
	result, err := s.projects.Create(r.Context(), upload.input)
	if err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	event.Slug = result.Project.Slug
	event.ProjectOwnerID = result.Project.OwnerID
	event.ExpandedBytes = result.Files.Bytes
	event.FileCount = result.Files.Count
	event.Status = "succeeded"
	event.Message = "站点发布成功"
	writeJSON(w, http.StatusCreated, s.toProjectResponse(r, result.Project))
}

// GetProject 返回单个项目。
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug) {
	value, err := s.projects.Get(r.Context(), slug)
	if err != nil {
		s.writeProjectError(w, r, err)
		return
	}
	grant, automation := deploymentGrant(r)
	if !canManageProject(s.requestUser(r), value) || automation && (value.ID != grant.ProjectID || value.OwnerID != grant.OwnerID) {
		s.writeProjectError(w, r, project.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.toProjectResponse(r, value))
}

// DeleteProject 删除项目及对应发布文件，并保留独立操作日志。
func (s *Server) DeleteProject(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug) {
	event := newActivity(r, "delete")
	event.Slug = slug
	defer s.finishActivity(r, event)
	user := s.requestUser(r)
	value, err := s.projects.Get(r.Context(), slug)
	if err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	if !canManageProject(user, value) {
		event.Message = s.writeProjectError(w, r, project.ErrNotFound)
		return
	}
	event.ProjectOwnerID = value.OwnerID
	if err := s.projects.DeleteAs(r.Context(), slug, project.Actor{ID: user.ID, IsAdmin: auth.IsAdmin(user.Role)}); err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	event.Status = "succeeded"
	event.Message = "项目已删除"
	w.WriteHeader(http.StatusNoContent)
}

// writeProjectError 输出可安全展示的原因，供页面和操作日志共用。
func (s *Server) writeProjectError(w http.ResponseWriter, r *http.Request, err error) string {
	code, message := http.StatusInternalServerError, "项目操作失败，请根据请求编号查看服务日志"
	var tooLarge *http.MaxBytesError
	switch {
	case errors.Is(err, storage.ErrInsufficient):
		code, message = http.StatusInsufficientStorage, uploadStorageReason(s.requestUser(r), false)
	case errors.Is(err, storage.ErrUnavailable):
		code, message = http.StatusServiceUnavailable, uploadStorageReason(s.requestUser(r), true)
	case errors.Is(err, errUploadTooLarge), errors.As(err, &tooLarge):
		code, message = http.StatusRequestEntityTooLarge, fmt.Sprintf("压缩包不能超过 %d MiB", s.config.MaxUploadSize>>20)
	case errors.Is(err, context.Canceled):
		code, message = http.StatusRequestTimeout, "请求已取消"
	case errors.Is(err, context.DeadlineExceeded):
		code, message = http.StatusRequestTimeout, "请求处理超时，请稍后重试"
	case errors.Is(err, project.ErrInvalidName):
		code, message = http.StatusBadRequest, "项目名称不能为空，UTF-8 编码不能超过 256 字节"
	case errors.Is(err, project.ErrInvalidDescription):
		code, message = http.StatusBadRequest, "项目说明的 UTF-8 编码不能超过 4096 字节"
	case errors.Is(err, project.ErrExportUnavailable):
		code, message = http.StatusConflict, "部署文件缺失、异常或超过导出限制，请联系管理员检查"
	case errors.Is(err, project.ErrUsageUnavailable):
		code, message = http.StatusConflict, "部署文件缺失或异常，请刷新项目或联系管理员检查"
	case errors.Is(err, project.ErrInvalidSlug):
		code, message = http.StatusBadRequest, "Slug 只能包含小写字母、数字和连字符，长度 1-52，且不能以连字符开头或结尾"
	case errors.Is(err, project.ErrInvalidArchive):
		code, message = http.StatusBadRequest, "压缩包无效或不符合部署规则"
		var invalid *project.InvalidArchiveError
		if errors.As(err, &invalid) {
			message = invalid.Reason
		}
	case errors.Is(err, project.ErrDraftExists):
		code, message = http.StatusConflict, "已有待发布版本，请先发布或丢弃后再上传"
	case errors.Is(err, project.ErrInvalidNotes):
		code, message = http.StatusBadRequest, "更新说明的 UTF-8 编码不能超过 4096 字节"
	case errors.Is(err, project.ErrAlreadyExists):
		code, message = http.StatusConflict, "项目 slug 已存在或有尚未恢复的发布，请换一个 slug 或稍后重试"
	case errors.Is(err, project.ErrNotFound):
		code, message = http.StatusNotFound, "项目不存在"
	case errors.Is(err, project.ErrVersionConflict):
		code, message = http.StatusConflict, "项目版本已发生变化，请刷新项目列表后重试"
	case errors.Is(err, project.ErrNoPrevious):
		code, message = http.StatusConflict, "没有可回退的上一版文件"
	case errors.Is(err, project.ErrReleaseBusy):
		code, message = http.StatusConflict, "上次操作尚待恢复，请联系管理员检查并重启服务后重试"
	}
	if code >= 500 {
		slog.ErrorContext(r.Context(), "project request failed", "request_id", requestID(r), "error", err)
	}
	writeJSON(w, code, generated.ErrorResponse{Error: message})
	return message
}

// toProjectResponse 转换项目元数据与访问地址。
func (s *Server) toProjectResponse(r *http.Request, value project.Project) generated.Project {
	user := s.requestUser(r)
	usage, err := s.projects.FileUsageAs(r.Context(), value, project.Actor{ID: user.ID, IsAdmin: auth.IsAdmin(user.Role)})
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.WarnContext(r.Context(), "project file usage unavailable", "request_id", requestID(r), "slug", value.Slug, "error", err)
	}
	var description *string
	if value.Description != "" {
		description = &value.Description
	}

	deployments := make([]generated.Deployment, 0, len(value.Deployments))
	versions := append([]project.Deployment{}, value.Deployments...)
	if value.Draft != nil {
		versions = append(versions, *value.Draft)
	}
	var draft *generated.Deployment
	for _, item := range versions {
		var finishedAt *time.Time
		if !item.FinishedAt.IsZero() {
			finished := item.FinishedAt
			finishedAt = &finished
		}

		var deploymentError *string
		if item.Error != "" {
			message := item.Error
			deploymentError = &message
		}

		var files *generated.PublishedFiles
		if measured, ok := usage[item.ID]; ok {
			files = &generated.PublishedFiles{Bytes: measured.Bytes, Count: measured.Count}
		}
		previewURL := s.versionURL(r, item.ID)
		notes := item.Notes
		version := generated.Deployment{
			PreviewUrl: &previewURL, Notes: &notes,
			Files:      files,
			Id:         item.ID,
			Status:     generated.DeploymentStatus(item.Status),
			FileName:   item.FileName,
			CreatedAt:  item.CreatedAt,
			FinishedAt: finishedAt,
			Error:      deploymentError,
		}
		if item.Status == "draft" {
			draft = &version
		} else {
			deployments = append(deployments, version)
		}
	}

	var deploymentPointer *[]generated.Deployment
	if len(deployments) > 0 {
		deploymentPointer = &deployments
	}

	return generated.Project{
		Draft:       draft,
		Id:          value.ID,
		OwnerId:     value.OwnerID,
		OwnerEmail:  value.OwnerEmail,
		Name:        value.Name,
		Slug:        value.Slug,
		Description: description,
		Status:      generated.ProjectStatus(value.Status),
		SiteUrl:     s.siteURL(r, value.Slug),
		CreatedAt:   value.CreatedAt,
		UpdatedAt:   value.UpdatedAt,
		Deployments: deploymentPointer,
	}
}

// requestUser 读取会话身份；无认证模式只供单元测试使用。
func (s *Server) requestUser(r *http.Request) auth.User {
	if user, ok := r.Context().Value(authContextKey{}).(auth.User); ok {
		return user
	}
	if s.config.DisableAuthForTests {
		return auth.User{ID: "test-owner", Email: "test@example.com", Role: auth.RoleAdmin}
	}
	return auth.User{}
}

// canManageProject 使用数据库所有者 ID 判断项目是否属于当前用户。
func canManageProject(user auth.User, value project.Project) bool {
	return auth.IsAdmin(user.Role) || user.ID != "" && value.OwnerID == user.ID
}

// siteURL 在本地生成隔离预览，在公网生成项目子域名。
func (s *Server) siteURL(r *http.Request, slug string) string {
	scheme := requestScheme(r)
	if s.config.BaseDomain != "" && !isLoopbackHost(r.Host) {
		return fmt.Sprintf("%s://stack-nest-%s.%s", scheme, slug, s.config.BaseDomain)
	}
	return localSiteURL(r, slug)
}

// requestScheme 读取受入口代理覆盖的外部协议。
func requestScheme(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		if comma := strings.IndexByte(forwarded, ','); comma >= 0 {
			forwarded = forwarded[:comma]
		}
		switch strings.ToLower(strings.TrimSpace(forwarded)) {
		case "http":
			return "http"
		case "https":
			return "https"
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}
