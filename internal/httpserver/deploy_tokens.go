package httpserver

import (
	"errors"
	"net/http"

	"stacknest/server/internal/deploytoken"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
)

// tokenProject 仅允许已登录的项目管理者维护部署凭据。
func (s *Server) tokenProject(w http.ResponseWriter, r *http.Request) (project.Project, bool) {
	if s.config.DeployTokens == nil {
		writeJSON(w, 503, generated.ErrorResponse{Error: "自动部署尚未启用"})
		return project.Project{}, false
	}
	v, err := s.projects.Get(r.Context(), r.PathValue("slug"))
	if err == nil && !canManageProject(s.requestUser(r), v) {
		err = project.ErrNotFound
	}
	if err != nil {
		s.writeProjectError(w, r, err)
		return v, false
	}
	return v, true
}

// ListDeployTokens 不返回原始 Token 或哈希，只显示凭据管理所需的元数据。
func (s *Server) ListDeployTokens(w http.ResponseWriter, r *http.Request) {
	v, ok := s.tokenProject(w, r)
	if !ok {
		return
	}
	values, err := s.config.DeployTokens.ListDeployTokens(r.Context(), v.ID, s.requestUser(r))
	if err != nil {
		s.writeProjectError(w, r, err)
		return
	}
	writeJSON(w, 200, struct {
		Tokens []deploytoken.Token `json:"tokens"`
	}{values})
}

// CreateDeployToken 默认只允许上传预览，正式发布需要创建时明确勾选。
func (s *Server) CreateDeployToken(w http.ResponseWriter, r *http.Request) {
	v, ok := s.tokenProject(w, r)
	if !ok {
		return
	}
	var input struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expiresInDays"`
		CanPublish    bool   `json:"canPublish"`
	}
	if !readAuthJSON(w, r, &input) {
		return
	}
	if input.ExpiresInDays == 0 {
		input.ExpiresInDays = 30
	}
	token, plain, err := deploytoken.New(v.ID, input.Name, input.ExpiresInDays, input.CanPublish)
	if err != nil {
		writeJSON(w, 400, generated.ErrorResponse{Error: "名称须为 1–60 个字符，有效期须为 1–365 天"})
		return
	}
	event := newActivity(r, "token-create")
	event.Slug, event.ProjectOwnerID = v.Slug, v.OwnerID
	defer s.finishActivity(r, event)
	err = s.config.DeployTokens.CreateDeployToken(r.Context(), token, s.requestUser(r))
	if errors.Is(err, deploytoken.ErrLimit) {
		writeJSON(w, 409, generated.ErrorResponse{Error: "每个项目最多保留 10 个 Token，请先撤销不再使用的凭据"})
		return
	}
	if err != nil {
		s.writeProjectError(w, r, err)
		return
	}
	token.CreatedBy = s.requestUser(r).Email
	event.Status, event.Message = "succeeded", "已创建部署 Token："+token.Name
	writeJSON(w, 201, struct {
		Token  deploytoken.Token `json:"token"`
		Secret string            `json:"secret"`
	}{token, plain})
}

// DeleteDeployToken 撤销后续自动部署请求，不改变已上线的站点和已有预览。
func (s *Server) DeleteDeployToken(w http.ResponseWriter, r *http.Request) {
	v, ok := s.tokenProject(w, r)
	if !ok {
		return
	}
	event := newActivity(r, "token-revoke")
	event.Slug, event.ProjectOwnerID = v.Slug, v.OwnerID
	defer s.finishActivity(r, event)
	if err := s.config.DeployTokens.DeleteDeployToken(r.Context(), v.ID, r.PathValue("id"), s.requestUser(r)); err != nil {
		s.writeProjectError(w, r, err)
		return
	}
	event.Status, event.Message = "succeeded", "已撤销部署 Token："+r.PathValue("id")
	w.WriteHeader(204)
}
