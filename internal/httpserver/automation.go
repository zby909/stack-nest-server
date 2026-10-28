package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/deploytoken"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
)

const deployPrefix = "/api/deploy/v1/"

type deployContextKey struct{}

// deploymentGrant 读取仅由自动部署入口注入的授权，不信任请求体中的项目或身份。
func deploymentGrant(r *http.Request) (deploytoken.Grant, bool) {
	g, ok := r.Context().Value(deployContextKey{}).(deploytoken.Grant)
	return g, ok
}

// uploadOwner 隔离不同 Token 与浏览器的续传记录，防止凭据接管已有上传。
func (s *Server) uploadOwner(r *http.Request) string {
	if g, ok := deploymentGrant(r); ok {
		return "deploy:" + g.ID
	}
	return s.requestUser(r).ID
}

// deploymentActor 在业务锁内再次校验不可变项目和所有者，管理员 Token 也不能扩大范围。
func (s *Server) deploymentActor(r *http.Request) project.Actor {
	if g, ok := deploymentGrant(r); ok {
		return project.Actor{ID: g.OwnerID, ProjectID: g.ProjectID}
	}
	u := s.requestUser(r)
	return project.Actor{ID: u.ID, IsAdmin: auth.IsAdmin(u.Role)}
}

// automationBoundary 单独隔离 Bearer API；其他接口继续要求登录 Cookie 和同源检查。
func (s *Server) automationBoundary(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+deployPrefix+"project", func(w http.ResponseWriter, r *http.Request) {
		g, _ := deploymentGrant(r)
		s.GetProject(w, r, g.Slug)
	})
	mux.HandleFunc("POST "+deployPrefix+"uploads", s.CreateUpload)
	mux.HandleFunc("GET "+deployPrefix+"uploads/{id}", func(w http.ResponseWriter, r *http.Request) { s.GetUpload(w, r, r.PathValue("id")) })
	mux.HandleFunc("DELETE "+deployPrefix+"uploads/{id}", func(w http.ResponseWriter, r *http.Request) { s.DeleteUpload(w, r, r.PathValue("id")) })
	mux.HandleFunc("PATCH "+deployPrefix+"uploads/{id}", func(w http.ResponseWriter, r *http.Request) {
		offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if err != nil || offset < 0 {
			writeJSON(w, 400, generated.ErrorResponse{Error: "请提供有效的 Upload-Offset"})
			return
		}
		s.AppendUpload(w, r, r.PathValue("id"), generated.AppendUploadParams{UploadOffset: offset})
	})
	mux.HandleFunc("POST "+deployPrefix+"uploads/{id}/complete", func(w http.ResponseWriter, r *http.Request) { s.CompleteUpload(w, r, r.PathValue("id")) })
	mux.HandleFunc("POST "+deployPrefix+"publish", func(w http.ResponseWriter, r *http.Request) {
		g, _ := deploymentGrant(r)
		if !g.CanPublish {
			writeJSON(w, 403, generated.ErrorResponse{Error: "此 Token 仅允许上传预览，请在管理页面确认发布"})
			return
		}
		s.PublishDraft(w, r, g.Slug)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, deployPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if s.config.DeployTokens == nil {
			writeJSON(w, 503, generated.ErrorResponse{Error: "自动部署尚未启用"})
			return
		}
		if requestScheme(r) != "https" && !isLoopbackHost(r.Host) {
			writeJSON(w, 403, generated.ErrorResponse{Error: "自动部署必须通过 HTTPS 或本机隧道访问"})
			return
		}
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			s.deploymentUnauthorized(w)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			s.deploymentUnauthorized(w)
			return
		}
		hash, err := deploytoken.Hash(parts[1])
		if err != nil {
			s.deploymentUnauthorized(w)
			return
		}
		g, err := s.config.DeployTokens.AuthenticateDeployToken(r.Context(), hash)
		if errors.Is(err, deploytoken.ErrInvalid) {
			s.deploymentUnauthorized(w)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "authenticate deployment token", "request_id", requestID(r), "error", err)
			writeJSON(w, 503, generated.ErrorResponse{Error: "暂时无法验证部署凭据"})
			return
		}
		ctx := context.WithValue(r.Context(), deployContextKey{}, g)
		ctx = context.WithValue(ctx, authContextKey{}, g.User)
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

// deploymentUnauthorized 使用统一错误，不透露凭据是否过期、撤销或失去项目权限。
func (s *Server) deploymentUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="stacknest-deploy"`)
	writeJSON(w, 401, generated.ErrorResponse{Error: "部署 Token 无效、已过期或已失去权限"})
}
