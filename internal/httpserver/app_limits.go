package httpserver

import (
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/containerapp"
	"time"
)

// appLimitsUser 在真实认证和测试入口统一检查超管身份。
func (s *Server) appLimitsUser(w http.ResponseWriter, r *http.Request) bool {
	if s.auth == nil || s.requestUser(r).Role != auth.RoleSuperAdmin {
		writeJSON(w, 403, map[string]string{"error": "仅超级管理员可配置应用资源"})
		return false
	}
	return s.appsReady(w)
}

// GetAppLimits 只向超管返回实际预留量及宿主允许的预算上限。
func (s *Server) GetAppLimits(w http.ResponseWriter, r *http.Request) {
	if !s.appLimitsUser(w, r) {
		return
	}
	view, err := s.config.Apps.LimitSettings(r.Context())
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 200, view)
}

// UpdateAppLimits 校验同源、角色和乐观版本后保存实例预算。
func (s *Server) UpdateAppLimits(w http.ResponseWriter, r *http.Request) {
	if !s.appLimitsUser(w, r) {
		return
	}
	var input containerapp.LimitsUpdate
	if !readAuthJSON(w, r, &input) {
		return
	}
	view, err := s.config.Apps.UpdateLimits(r.Context(), input)
	user := s.requestUser(r)
	if s.config.Activity != nil {
		status, message := "succeeded", "已更新 Docker 应用资源预算"
		if err != nil {
			status, message = "failed", "Docker 应用资源预算更新失败"
		}
		if logErr := s.config.Activity.Append(activity.Event{ID: uuid.NewString(), CreatedAt: time.Now().UTC(), Action: "app-limits", Actor: user.Email, ActorID: user.ID, Status: status, Message: message}); logErr != nil {
			slog.Error("record app limits update", "error", logErr)
		}
	}
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 200, view)
}
