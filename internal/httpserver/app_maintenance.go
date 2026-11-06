package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
)

// GetAppDeployments 复用当前应用归属，普通用户只能读取自己的最近部署。
func (s *Server) GetAppDeployments(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w) {
		return
	}
	items, err := s.config.Apps.History(r.Context(), r.PathValue("id"), s.appActor(r))
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deployments": items})
}

// appImagesUser 在处理请求前验证超管，宿主镜像清单不向普通管理员开放。
func (s *Server) appImagesUser(w http.ResponseWriter, r *http.Request) bool {
	if s.auth == nil || s.requestUser(r).Role != auth.RoleSuperAdmin {
		writeJSON(w, 403, map[string]string{"error": "仅超级管理员可管理镜像空间"})
		return false
	}
	return s.appsReady(w)
}

// GetAppImages 按需读取镜像空间及清理资格。
func (s *Server) GetAppImages(w http.ResponseWriter, r *http.Request) {
	if !s.appImagesUser(w, r) {
		return
	}
	value, err := s.config.Apps.Images(r.Context())
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 200, value)
}

// DeleteAppImage 同源和超管校验后重新检查引用，记录成功及失败结果。
func (s *Server) DeleteAppImage(w http.ResponseWriter, r *http.Request) {
	if !s.appImagesUser(w, r) {
		return
	}
	id := r.PathValue("imageId")
	err := s.config.Apps.DeleteImage(r.Context(), id)
	if s.config.Activity != nil {
		user := s.requestUser(r)
		status, message := "succeeded", "已清理未使用镜像 "+id
		if err != nil {
			status, message = "failed", "镜像清理未完成"
		}
		if logErr := s.config.Activity.Append(activity.Event{ID: uuid.NewString(), CreatedAt: time.Now().UTC(), Action: "app-image-delete", Actor: user.Email, ActorID: user.ID, Status: status, Message: message}); logErr != nil {
			slog.Error("record image cleanup", "error", logErr)
		}
	}
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}
