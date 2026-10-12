package httpserver

import (
	"errors"
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
	"time"
)

// ChangeUserRole 仅超管可以修改现有用户角色，旧页面和受保护账号均不能被覆盖。
func (s *Server) ChangeUserRole(w http.ResponseWriter, r *http.Request) {
	actor := s.requestUser(r)
	if actor.Role != auth.RoleSuperAdmin {
		writeJSON(w, 403, generated.ErrorResponse{Error: "仅超级管理员可调整角色"})
		return
	}
	var input struct {
		Role         string `json:"role"`
		ExpectedRole string `json:"expectedRole"`
	}
	if !readAuthJSON(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil || (input.Role != auth.RoleAdmin && input.Role != auth.RoleUser) || (input.ExpectedRole != auth.RoleAdmin && input.ExpectedRole != auth.RoleUser) || input.Role == input.ExpectedRole {
		writeJSON(w, 400, generated.ErrorResponse{Error: "请选择不同的有效角色"})
		return
	}
	user, err := s.auth.ChangeUserRole(r.Context(), actor, id, input.ExpectedRole, input.Role)
	switch {
	case errors.Is(err, auth.ErrForbidden):
		writeJSON(w, 403, generated.ErrorResponse{Error: "部署配置指定的管理员和超级管理员不可调整角色"})
	case errors.Is(err, auth.ErrRoleConflict):
		writeJSON(w, 409, generated.ErrorResponse{Error: "角色已变化，请刷新用户列表后重试"})
	case errors.Is(err, auth.ErrNotFound):
		writeJSON(w, 404, generated.ErrorResponse{Error: "用户不存在"})
	case err != nil:
		slog.ErrorContext(r.Context(), "change user role", "error", err)
		writeJSON(w, 500, generated.ErrorResponse{Error: "角色修改失败"})
	default:
		if s.config.Activity != nil {
			if logErr := s.config.Activity.Append(activity.Event{ID: uuid.NewString(), CreatedAt: time.Now().UTC(), Action: "user-role", Actor: actor.Email, ActorID: actor.ID, Status: "succeeded", Message: "已调整登录邮箱：" + user.Email + "，角色：" + input.ExpectedRole + " → " + user.Role + "；原会话已撤销"}); logErr != nil {
				slog.Error("record changed user role", "error", logErr)
			}
		}
		writeJSON(w, 200, authUserResponse(user))
	}
}
