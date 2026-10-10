package httpserver

import (
	"net/http"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
)

// ListProjectFiles 返回所选版本的文件清单，普通用户只能查看自己的项目。
func (s *Server) ListProjectFiles(w http.ResponseWriter, r *http.Request, slug generated.ProjectSlug, params generated.ListProjectFilesParams) {
	user := s.requestUser(r)
	files, err := s.projects.FilesAs(r.Context(), slug, project.Actor{ID: user.ID, IsAdmin: user.Role == auth.RoleAdmin}, params.DeploymentId)
	if err != nil {
		s.writeProjectError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Files []project.PublishedFile `json:"files"`
	}{Files: files})
}
