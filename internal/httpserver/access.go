package httpserver

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
)

// SetProjectAccess 校验邮箱权限及页面版本，暂停所有页面和资源的访问。
func (s *Server) SetProjectAccess(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug, params generated.SetProjectAccessParams) {
	value, err := s.projects.Get(r.Context(), slug)
	if err == nil && !canManageProject(s.requestUser(r), value) {
		err = project.ErrNotFound
	}
	if err != nil {
		s.writeProjectError(w, r, err)
		return
	}
	match, err := strconv.Unquote(params.IfMatch)
	expectedAt, parseErr := time.Parse(time.RFC3339Nano, match)
	if err != nil || parseErr != nil || !strings.HasPrefix(params.IfMatch, "\"") {
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: "If-Match 必须是带双引号的项目更新时间"})
		return
	}
	var body struct {
		Paused *bool `json:"paused"`
	}
	if !readAuthJSON(w, r, &body) {
		return
	}
	if body.Paused == nil {
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: "必须指定是否暂停访问"})
		return
	}
	action, message := "resume", "站点已恢复访问"
	if *body.Paused {
		action, message = "pause", "站点已暂停访问，文件和版本保留"
	}
	event := newActivity(r, action)
	event.Slug, event.ProjectOwnerID = slug, value.OwnerID
	defer s.finishActivity(r, event)
	user := s.requestUser(r)
	updated, err := s.projects.SetAccessAs(r.Context(), slug, project.Actor{ID: user.ID, IsAdmin: user.Role == auth.RoleAdmin}, *body.Paused, expectedAt)
	if err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	event.Status, event.Message = "succeeded", message
	writeJSON(w, http.StatusOK, s.toProjectResponse(r, updated))
}
