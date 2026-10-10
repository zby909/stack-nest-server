package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
)

// PublishDraft 只在明确确认版本后上线，响应断线不会中断已经开始的切换。
func (s *Server) PublishDraft(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug) {
	var input struct {
		DraftID    *string `json:"draftId"`
		ExpectedID *string `json:"expectedId"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	var extra any
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || decoder.Decode(&input) != nil || input.DraftID == nil || input.ExpectedID == nil || decoder.Decode(&extra) != io.EOF {
		writeJSON(w, 400, generated.ErrorResponse{Error: "请提供待发布版本和当前正式版本编号"})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
	defer cancel()
	r = r.WithContext(ctx)
	event := newActivity(r, "publish")
	event.Slug = slug
	defer s.finishActivity(r, event)
	user := s.requestUser(r)
	result, err := s.projects.PublishDraftAs(ctx, slug, project.Actor{ID: user.ID, IsAdmin: user.Role == auth.RoleAdmin}, *input.DraftID, *input.ExpectedID)
	if err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	event.Status, event.Message, event.ProjectOwnerID = "succeeded", "待发布版本已正式发布", result.Project.OwnerID
	event.ExpandedBytes, event.FileCount = result.Files.Bytes, result.Files.Count
	writeJSON(w, 200, s.toProjectResponse(r, result.Project))
}

// DiscardDraft 按确切草稿编号删除，过期页面不能误删后来上传的版本。
func (s *Server) DiscardDraft(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug, params generated.DiscardDraftParams) {
	event := newActivity(r, "discard")
	event.Slug = slug
	defer s.finishActivity(r, event)
	user := s.requestUser(r)
	value, err := s.projects.DiscardDraftAs(r.Context(), slug, project.Actor{ID: user.ID, IsAdmin: user.Role == auth.RoleAdmin}, params.DraftId)
	if err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	event.Status, event.Message, event.ProjectOwnerID = "succeeded", "待发布版本已丢弃", value.OwnerID
	writeJSON(w, 200, s.toProjectResponse(r, value))
}

// ListProjectChanges 限时比较所选版本，返回有界的文件变化清单。
func (s *Server) ListProjectChanges(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug, params generated.ListProjectChangesParams) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	user := s.requestUser(r)
	base := ""
	if params.BaseId != nil {
		base = *params.BaseId
	}
	changes, err := s.projects.ChangesAs(ctx, slug, project.Actor{ID: user.ID, IsAdmin: user.Role == auth.RoleAdmin}, params.DeploymentId, base)
	if err != nil {
		s.writeProjectError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, struct {
		Changes []project.FileChange `json:"changes"`
	}{Changes: changes})
}
