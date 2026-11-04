package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/deploytoken"
)

const appDeployPrefix = "/api/app-deploy/v1/"

// tokenAuthenticator 让两类凭据共用传输安全检查，但分别校验目标资源种类。
type tokenAuthenticator interface {
	AuthenticateDeployToken(context.Context, string) (deploytoken.Grant, error)
}

// authenticateDeployment 不读取 Cookie，不记录 Authorization，并对每次请求重新查验凭据。
func (s *Server) authenticateDeployment(w http.ResponseWriter, r *http.Request, store tokenAuthenticator) (deploytoken.Grant, bool) {
	if requestScheme(r) != "https" && !isLoopbackHost(r.Host) {
		writeJSON(w, 403, map[string]string{"error": "自动部署必须通过 HTTPS 或本机隧道访问"})
		return deploytoken.Grant{}, false
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		s.deploymentUnauthorized(w)
		return deploytoken.Grant{}, false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		s.deploymentUnauthorized(w)
		return deploytoken.Grant{}, false
	}
	hash, err := deploytoken.Hash(parts[1])
	if err != nil {
		s.deploymentUnauthorized(w)
		return deploytoken.Grant{}, false
	}
	grant, err := store.AuthenticateDeployToken(r.Context(), hash)
	if errors.Is(err, deploytoken.ErrInvalid) {
		s.deploymentUnauthorized(w)
		return grant, false
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "authenticate deployment token", "request_id", requestID(r), "error", err)
		writeJSON(w, 503, map[string]string{"error": "暂时无法验证部署凭据"})
		return grant, false
	}
	return grant, true
}

// appAutomationBoundary 只提供绑定应用的状态与镜像更新，不能调用后台或静态发布 API。
func (s *Server) appAutomationBoundary(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+appDeployPrefix+"app", s.automationAppStatus)
	mux.HandleFunc("POST "+appDeployPrefix+"deploy", s.automationAppDeploy)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, appDeployPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if s.config.AppDeployTokens == nil || s.config.Apps == nil {
			writeJSON(w, 503, map[string]string{"error": "Docker 自动部署尚未启用"})
			return
		}
		grant, ok := s.authenticateDeployment(w, r, s.config.AppDeployTokens)
		if !ok {
			return
		}
		if grant.AppID == "" || grant.ProjectID != "" {
			s.deploymentUnauthorized(w)
			return
		}
		ctx := context.WithValue(r.Context(), deployContextKey{}, grant)
		ctx = context.WithValue(ctx, authContextKey{}, grant.User)
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

// automationAppResponse 返回有界任务摘要，不包含环境变量、凭据或完整容器配置。
func (s *Server) automationAppResponse(r *http.Request, app containerapp.App) map[string]any {
	value := map[string]any{"appId": app.ID, "slug": app.Slug, "name": app.Name, "image": app.Image, "generation": app.Generation, "status": app.Status, "url": s.appURL(r, app.Slug), "error": app.Error, "canceled": app.Canceled}
	if app.Status == "pending" {
		value["stage"] = "queued"
	}
	if app.Status == "deploying" {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if progress, err := s.config.Apps.Runtime().Progress(ctx, app.ID, app.Generation); err == nil && progress.Generation == app.Generation {
			value["stage"] = progress.Stage
		}
	}
	return value
}

// automationAppStatus 的可选代次必须一致，防止把后来的部署误认为本次成功。
func (s *Server) automationAppStatus(w http.ResponseWriter, r *http.Request) {
	grant, _ := deploymentGrant(r)
	app, err := s.config.Apps.Get(r.Context(), grant.AppID, containerapp.Actor{ID: grant.OwnerID})
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	if values, exists := r.URL.Query()["generation"]; exists {
		generation, parseErr := strconv.ParseInt(r.URL.Query().Get("generation"), 10, 64)
		if len(values) != 1 || parseErr != nil || generation < 1 {
			writeJSON(w, 400, map[string]string{"error": "任务代次无效"})
			return
		}
		if generation != app.Generation {
			s.writeAppError(w, r, containerapp.ErrConflict)
			return
		}
	}
	writeJSON(w, 200, s.automationAppResponse(r, app))
}

// automationAppDeploy 只提交持久化任务，请求断开不会中止已经接受的部署。
func (s *Server) automationAppDeploy(w http.ResponseWriter, r *http.Request) {
	var input containerapp.ImageUpdate
	if !readAuthJSON(w, r, &input) {
		return
	}
	grant, _ := deploymentGrant(r)
	app, err := s.config.Apps.DeployImage(r.Context(), grant, input)
	if errors.Is(err, deploytoken.ErrInvalid) {
		s.deploymentUnauthorized(w)
		return
	}
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 202, s.automationAppResponse(r, app))
}
