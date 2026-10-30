package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/containerapp"
	"strings"
	"sync"
	"time"
)

// appActor 把后台会话转换为应用所有权限制。
func (s *Server) appActor(r *http.Request) containerapp.Actor {
	user := s.requestUser(r)
	return containerapp.Actor{ID: user.ID, Email: user.Email, Admin: auth.IsAdmin(user.Role)}
}

// appsReady 未配置运行器时给出明确提示，而非访问 Docker daemon。
func (s *Server) appsReady(w http.ResponseWriter) bool {
	if s.config.Apps == nil {
		writeJSON(w, 503, map[string]string{"error": "当前环境未启用 Docker 应用"})
		return false
	}
	return true
}

// appURL 使用不与静态 slug 冲突的命名空间，同时支持 SSH 隧道。
func (s *Server) appURL(r *http.Request, slug string) string {
	base, port := s.config.BaseDomain, ""
	if base == "" || isLoopbackHost(r.Host) {
		base = "localhost"
		_, port = splitHost(r.Host)
	}
	host := containerapp.HostPrefix + slug + "." + base
	if port != "" {
		host = net.JoinHostPort(host, port)
	}
	return requestScheme(r) + "://" + host + "/"
}

// appResponse 填充应用域名和实时容器状态，不输出完整 Docker 配置。
func (s *Server) appResponse(r *http.Request, value containerapp.App) map[string]any {
	value.URL = s.appURL(r, value.Slug)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	response := map[string]any{"app": value}
	// 先读取独立的进度快照，容器状态采样变慢时仍保留阶段反馈。
	if value.Status == "pending" {
		response["progress"] = containerapp.Progress{Generation: value.Generation, Stage: "queued", CanCancel: value.Action == "deploy", UpdatedAt: value.UpdatedAt}
	} else if value.Status == "deploying" || value.Status == "error" {
		if progress, progressErr := s.config.Apps.Runtime().Progress(ctx, value.ID, value.Generation); progressErr == nil && progress.Generation == value.Generation {
			response["progress"] = progress
		}
	}
	if state, err := s.config.Apps.State(ctx, value); err == nil {
		response["runtime"] = state
	}
	return response
}

// ListApps 返回自己的应用；管理员可以查看全体，状态不可用时保留配置列表。
func (s *Server) ListApps(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w) {
		return
	}
	values, err := s.config.Apps.List(r.Context(), s.appActor(r))
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	views := make([]map[string]any, len(values))
	var wait sync.WaitGroup
	for i, value := range values {
		wait.Add(1)
		go func() {
			defer wait.Done()
			views[i] = s.appResponse(r, value)
		}()
	}
	wait.Wait()
	limits, err := s.config.Apps.Limits(r.Context())
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	available := s.config.Apps.Runtime().Health(r.Context()) == nil
	writeJSON(w, 200, map[string]any{"apps": views, "available": available, "limits": map[string]int{"maxApps": limits.MaxApps, "totalMemoryMiB": limits.TotalMemoryMiB, "maxMemoryMiB": containerapp.MaxMemoryMiB}})
}

// CreateApp 提交持久化后台部署任务，浏览器断开不影响执行。
func (s *Server) CreateApp(w http.ResponseWriter, r *http.Request) { s.saveApp(w, r, "") }

// UpdateApp 对编辑采用持久化代次，拒绝旧页面覆盖其他更新。
func (s *Server) UpdateApp(w http.ResponseWriter, r *http.Request, id string) { s.saveApp(w, r, id) }

// saveApp 有界读取完整应用配置，输入不会写入操作日志。
func (s *Server) saveApp(w http.ResponseWriter, r *http.Request, id string) {
	if !s.appsReady(w) {
		return
	}
	var input struct {
		containerapp.Input
		Generation int64 `json:"generation"`
	}
	if !readAppJSON(w, r, &input) {
		return
	}
	value, err := s.config.Apps.Save(r.Context(), id, s.appActor(r), input.Input, input.Generation)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 202, s.appResponse(r, value))
}

// GetApp 返回当前配置及实时容器状态，权限在数据库层检查。
func (s *Server) GetApp(w http.ResponseWriter, r *http.Request, id string) {
	if !s.appsReady(w) {
		return
	}
	value, err := s.config.Apps.Get(r.Context(), id, s.appActor(r))
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	response := s.appResponse(r, value)
	limits, err := s.config.Apps.Limits(r.Context())
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	response["available"] = s.config.Apps.Runtime().Health(r.Context()) == nil
	response["limits"] = map[string]int{"maxApps": limits.MaxApps, "totalMemoryMiB": limits.TotalMemoryMiB, "maxMemoryMiB": containerapp.MaxMemoryMiB}
	writeJSON(w, 200, response)
}

// OperateApp 对启停、重启、删除执行相同身份和版本校验。
func (s *Server) OperateApp(w http.ResponseWriter, r *http.Request, id string) {
	if !s.appsReady(w) {
		return
	}
	var input struct {
		Action     string `json:"action"`
		Generation int64  `json:"generation"`
	}
	if !readAppJSON(w, r, &input) {
		return
	}
	value, err := s.config.Apps.Queue(r.Context(), id, s.appActor(r), input.Action, input.Generation)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 202, s.appResponse(r, value))
}

// GetAppLogs 在确认权限后返回文本，页面不会把程序输出作为 HTML 执行。
func (s *Server) GetAppLogs(w http.ResponseWriter, r *http.Request, id string) {
	if !s.appsReady(w) {
		return
	}
	value, err := s.config.Apps.Get(r.Context(), id, s.appActor(r))
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	text, err := s.config.Apps.Logs(r.Context(), value)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"text": text})
}

// writeAppError 只向用户返回受控错误，数据库与连接内部信息留在服务日志。
func (s *Server) writeAppError(w http.ResponseWriter, r *http.Request, err error) {
	status, message := 500, "Docker 应用操作暂时失败"
	switch {
	case errors.Is(err, containerapp.ErrNotFound):
		status, message = 404, err.Error()
	case errors.Is(err, containerapp.ErrBusy), errors.Is(err, containerapp.ErrQuota), errors.Is(err, containerapp.ErrConflict), errors.Is(err, containerapp.ErrCancelTooLate):
		status, message = 409, err.Error()
	case errors.Is(err, containerapp.ErrUnavailable):
		status, message = 503, err.Error()
	case errors.Is(err, containerapp.ErrInvalid):
		status, message = 400, strings.TrimPrefix(err.Error(), containerapp.ErrInvalid.Error()+": ")
	default:
		slog.ErrorContext(r.Context(), "container app request", "error", err)
	}
	writeJSON(w, status, map[string]string{"error": message})
}
