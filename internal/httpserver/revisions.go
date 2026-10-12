package httpserver

import (
	"net/http"
	"strconv"
	"strings"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

// UpdateProject 在读取上传内容前检查项目归属、当前部署及磁盘状态。
func (s *Server) UpdateProject(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug, params generated.UpdateProjectParams) {
	s.reviseProject(w, r, slug, params.IfMatch, false)
}

// RollbackProject 回退上一版，无需再次上传或额外复制整份站点。
func (s *Server) RollbackProject(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug, params generated.RollbackProjectParams) {
	s.reviseProject(w, r, slug, params.IfMatch, true)
}

// reviseProject 共用权限、并发、日志和过期版本校验，避免更新与回退的行为分叉。
func (s *Server) reviseProject(w http.ResponseWriter, r *http.Request, slug, match string, revert bool) {
	action := "update"
	if revert {
		action = "rollback"
	}
	event := newActivity(r, action)
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
	expectedID, err := strconv.Unquote(match)
	if err != nil || expectedID == "" || !strings.HasPrefix(match, "\"") {
		event.Message = "请提供带双引号的当前部署编号 If-Match"
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: event.Message})
		return
	}
	if len(value.Deployments) == 0 || value.Deployments[0].ID != expectedID {
		event.Message = s.writeProjectError(w, r, project.ErrVersionConflict)
		return
	}
	if !s.uploadMu.TryLock() {
		event.Message = "另一个发布正在处理中，请稍后重试"
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusTooManyRequests, generated.ErrorResponse{Error: event.Message})
		return
	}
	defer s.uploadMu.Unlock()
	user := s.requestUser(r)
	actor := project.Actor{ID: user.ID, IsAdmin: auth.IsAdmin(user.Role)}
	var result project.CreatedProject
	if revert {
		result, err = s.projects.RollbackAs(r.Context(), slug, actor, expectedID)
	} else {
		if err := s.config.Storage.Check(storage.UploadBudget); err != nil {
			event.Message = s.writeProjectError(w, r, err)
			return
		}
		if r.ContentLength > s.config.MaxUploadSize+(1<<20) {
			event.Message = s.writeProjectError(w, r, errUploadTooLarge)
			return
		}
		upload, receiveErr := s.receiveUpload(w, r, event, true)
		if receiveErr != nil {
			event.Message = s.writeProjectError(w, r, receiveErr)
			return
		}
		defer upload.close()
		result, err = s.projects.UpdateAs(r.Context(), slug, actor, project.UpdateInput{ExpectedID: expectedID, FileName: upload.input.FileName, Source: upload.input.Source})
	}
	if err != nil {
		event.Message = s.writeProjectError(w, r, err)
		return
	}
	event.Status = "succeeded"
	event.Message = "项目已更新，上一版已保留"
	if revert {
		event.Message = "项目已回退，原当前版已保留为上一版"
	}
	event.FileName = result.Project.Deployments[0].FileName
	event.ExpandedBytes, event.FileCount = result.Files.Bytes, result.Files.Count
	writeJSON(w, http.StatusOK, s.toProjectResponse(r, result.Project))
}
