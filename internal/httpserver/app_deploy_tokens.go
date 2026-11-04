package httpserver

import (
	"errors"
	"net/http"

	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/deploytoken"
)

// appTokenTarget 只有已登录且能管理应用的用户可签发或撤销自动部署权限。
func (s *Server) appTokenTarget(w http.ResponseWriter, r *http.Request) (containerapp.App, bool) {
	if !s.appsReady(w) {
		return containerapp.App{}, false
	}
	if s.config.AppDeployTokens == nil {
		writeJSON(w, 503, map[string]string{"error": "Docker 自动部署尚未启用"})
		return containerapp.App{}, false
	}
	app, err := s.config.Apps.Get(r.Context(), r.PathValue("id"), s.appActor(r))
	if err != nil {
		s.writeAppError(w, r, err)
		return app, false
	}
	return app, true
}

// ListAppDeployTokens 展示凭据的用途、有效期和最近使用时间，不可再次查看原文。
func (s *Server) ListAppDeployTokens(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appTokenTarget(w, r)
	if !ok {
		return
	}
	values, err := s.config.AppDeployTokens.ListAppDeployTokens(r.Context(), app.ID, s.requestUser(r))
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tokens": values})
}

// CreateAppDeployToken 明确授权直接更新正式应用，返回值只在本次响应包含随机凭据。
func (s *Server) CreateAppDeployToken(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appTokenTarget(w, r)
	if !ok {
		return
	}
	var input struct {
		Name          string `json:"name"`
		ExpiresInDays int    `json:"expiresInDays"`
	}
	if !readAuthJSON(w, r, &input) {
		return
	}
	if input.ExpiresInDays == 0 {
		input.ExpiresInDays = 30
	}
	token, plain, err := deploytoken.NewApp(app.ID, input.Name, input.ExpiresInDays)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "名称须为 1–60 个字符，有效期须为 1–365 天"})
		return
	}
	event := newActivity(r, "token-create")
	event.Slug, event.ProjectOwnerID = app.Slug, app.OwnerID
	defer s.finishActivity(r, event)
	err = s.config.AppDeployTokens.CreateAppDeployToken(r.Context(), token, s.requestUser(r))
	if errors.Is(err, deploytoken.ErrLimit) {
		writeJSON(w, 409, map[string]string{"error": "每个应用最多保留 10 个 Token，请先撤销不再使用的凭据"})
		return
	}
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	token.CreatedBy = s.requestUser(r).Email
	event.Status, event.Message = "succeeded", "已创建应用部署 Token："+token.Name
	writeJSON(w, 201, map[string]any{"token": token, "secret": plain})
}

// DeleteAppDeployToken 撤销后续请求；已接受的后台任务保持原有生命周期。
func (s *Server) DeleteAppDeployToken(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appTokenTarget(w, r)
	if !ok {
		return
	}
	event := newActivity(r, "token-revoke")
	event.Slug, event.ProjectOwnerID = app.Slug, app.OwnerID
	defer s.finishActivity(r, event)
	if err := s.config.AppDeployTokens.DeleteAppDeployToken(r.Context(), app.ID, r.PathValue("tokenId"), s.requestUser(r)); err != nil {
		s.writeAppError(w, r, err)
		return
	}
	event.Status, event.Message = "succeeded", "已撤销应用部署 Token："+r.PathValue("tokenId")
	w.WriteHeader(204)
}
