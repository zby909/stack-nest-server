package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
)

const sessionCookie = "stacknest_session"

// sessionCookieName 在 HTTPS 使用浏览器强制主机隔离的名称，本地 HTTP 保留开发会话。
func sessionCookieName(r *http.Request) string {
	if requestScheme(r) == "https" {
		return "__Host-" + sessionCookie
	}
	return sessionCookie
}

type authContextKey struct{}

type loginCounter struct {
	count int
	reset time.Time
}
type loginLimiter struct {
	mu       sync.Mutex
	counters map[string]loginCounter
}

// newLoginLimiter 创建按客户端 IP 计数的短时登录限流器。
func newLoginLimiter() *loginLimiter { return &loginLimiter{counters: make(map[string]loginCounter)} }

// allow 在一分钟内限制同一客户端对登录端点的请求数量。
func (l *loginLimiter) allow(key string, max int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.counters) > 2048 {
		for key, counter := range l.counters {
			if now.After(counter.reset) {
				delete(l.counters, key)
			}
		}
	}
	counter := l.counters[key]
	if now.After(counter.reset) {
		counter = loginCounter{reset: now.Add(time.Minute)}
	}
	if counter.count >= max {
		return false
	}
	counter.count++
	l.counters[key] = counter
	return true
}

// protect 对项目及邮箱管理接口验证服务端会话和管理员权限。
func (s *Server) protect(next http.Handler) http.Handler {
	if s.auth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if strings.HasPrefix(path, "/api/") && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !sameOrigin(r) {
				writeJSON(w, http.StatusForbidden, generated.ErrorResponse{Error: "请求来源不受信任"})
				return
			}
		}
		if strings.HasPrefix(path, "/api/") && path != "/api/health" && !strings.HasPrefix(path, "/api/auth/") {
			user, err := s.currentUser(r)
			if err != nil {
				if !errors.Is(err, auth.ErrNotFound) {
					slog.ErrorContext(r.Context(), "load auth session", "error", err)
					writeJSON(w, http.StatusInternalServerError, generated.ErrorResponse{Error: "暂时无法验证登录状态"})
					return
				}
				writeJSON(w, http.StatusUnauthorized, generated.ErrorResponse{Error: "请先登录"})
				return
			}
			if (strings.HasPrefix(path, "/api/users") || path == "/api/system" || strings.HasPrefix(path, "/api/backups")) && user.Role != auth.RoleAdmin {
				writeJSON(w, http.StatusForbidden, generated.ErrorResponse{Error: "仅管理员可访问"})
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, user))
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin 校验写请求的 Origin 或 Referer，防止跨站使用会话 Cookie。
func sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	source := r.Header.Get("Origin")
	if source == "" {
		source = r.Header.Get("Referer")
	}
	if source == "" {
		return false
	}
	parsed, err := url.Parse(source)
	return err == nil && strings.EqualFold(parsed.Scheme, requestScheme(r)) && strings.EqualFold(parsed.Host, r.Host) && parsed.User == nil
}

// currentUser 从 HttpOnly Cookie 加载当前会话。
func (s *Server) currentUser(r *http.Request) (auth.User, error) {
	cookie, err := r.Cookie(sessionCookieName(r))
	if err != nil {
		return auth.User{}, auth.ErrNotFound
	}
	return s.auth.CurrentUser(r.Context(), cookie.Value)
}

// setSessionCookie 写入仅主机可用的严格同站会话 Cookie。
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName(r), Value: token, Path: "/", HttpOnly: true,
		Secure: requestScheme(r) == "https", SameSite: http.SameSiteStrictMode,
		MaxAge: maxAge,
	})
}

// readAuthJSON 严格读取小体积 JSON 登录请求。
func readAuthJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, generated.ErrorResponse{Error: "请发送 JSON 请求"})
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: "请求内容无效"})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: "请求内容无效"})
		return false
	}
	return true
}

// authUserResponse 将业务用户转换为 OpenAPI 响应类型。
func authUserResponse(user auth.User) generated.AuthUser {
	return generated.AuthUser{Id: user.ID, Email: openapi_types.Email(user.Email), Role: generated.AuthUserRole(user.Role), CreatedAt: user.CreatedAt}
}

// requestIP 返回反向代理记录的客户端 IP；直连时使用远端地址。
func requestIP(r *http.Request) string {
	if value := net.ParseIP(r.Header.Get("X-Real-IP")); value != nil {
		return value.String()
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

// RequestLoginCode 请求邮件验证码，不暴露邮箱是否在白名单中。
func (s *Server) RequestLoginCode(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("request:"+requestIP(r), 10) {
		writeJSON(w, http.StatusTooManyRequests, generated.ErrorResponse{Error: "请求过于频繁，请稍后重试"})
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if !readAuthJSON(w, r, &input) {
		return
	}
	if _, err := auth.NormalizeEmail(input.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: "邮箱格式无效"})
		return
	}
	if err := s.auth.RequestCode(r.Context(), input.Email); err != nil {
		slog.ErrorContext(r.Context(), "send login code failed", "error", err)
	}
	w.WriteHeader(http.StatusAccepted)
}

// VerifyLoginCode 校验一次性验证码并建立会话。
func (s *Server) VerifyLoginCode(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("verify:"+requestIP(r), 15) {
		writeJSON(w, http.StatusTooManyRequests, generated.ErrorResponse{Error: "请求过于频繁，请稍后重试"})
		return
	}
	var input struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if !readAuthJSON(w, r, &input) {
		return
	}
	user, token, err := s.auth.VerifyCode(r.Context(), input.Email, input.Code)
	if errors.Is(err, auth.ErrInvalidCode) {
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: "验证码无效或已过期"})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "verify login code failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, generated.ErrorResponse{Error: "登录失败"})
		return
	}
	setSessionCookie(w, r, token, int(auth.SessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, authUserResponse(user))
}

// GetAuthSession 返回当前登录用户。
func (s *Server) GetAuthSession(w http.ResponseWriter, r *http.Request) {
	user, err := s.currentUser(r)
	if err != nil {
		if !errors.Is(err, auth.ErrNotFound) {
			slog.ErrorContext(r.Context(), "get auth session", "error", err)
			writeJSON(w, http.StatusInternalServerError, generated.ErrorResponse{Error: "暂时无法验证登录状态"})
			return
		}
		writeJSON(w, http.StatusUnauthorized, generated.ErrorResponse{Error: "请先登录"})
		return
	}
	writeJSON(w, http.StatusOK, authUserResponse(user))
}

// Logout 撤销会话并删除浏览器 Cookie。
func (s *Server) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName(r)); err == nil {
		if err := s.auth.Logout(r.Context(), cookie.Value); err != nil {
			slog.ErrorContext(r.Context(), "logout failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, generated.ErrorResponse{Error: "退出登录失败"})
			return
		}
	}
	setSessionCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

// ListUsers 返回邮箱白名单。
func (s *Server) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "list users", "error", err)
		writeJSON(w, http.StatusInternalServerError, generated.ErrorResponse{Error: "用户列表加载失败"})
		return
	}
	response := generated.UserListResponse{Users: make([]generated.AuthUser, 0, len(users))}
	if loginURL, err := auth.NormalizeInvitationURL(s.config.InvitationLoginURL); err == nil {
		response.InvitationLoginUrl = &loginURL
	}
	for _, user := range users {
		response.Users = append(response.Users, authUserResponse(user))
	}
	writeJSON(w, http.StatusOK, response)
}

// AddUser 将普通用户邮箱加入白名单，并仅在管理员勾选后尝试发送邀请。
func (s *Server) AddUser(w http.ResponseWriter, r *http.Request) {
	var input generated.AddUserRequest
	if !readAuthJSON(w, r, &input) {
		return
	}
	sendInvitation := input.SendInvitation != nil && *input.SendInvitation
	if sendInvitation {
		if _, err := auth.NormalizeInvitationURL(s.config.InvitationLoginURL); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, generated.ErrorResponse{Error: "邀请登录地址尚未配置，请取消勾选发送邀请后添加"})
			return
		}
	}
	user, err := s.auth.AddUser(r.Context(), string(input.Email))
	switch {
	case errors.Is(err, auth.ErrInvalidEmail):
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: "邮箱格式无效"})
	case errors.Is(err, auth.ErrExists):
		writeJSON(w, http.StatusConflict, generated.ErrorResponse{Error: "邮箱已在登录名单中"})
	case err != nil:
		slog.ErrorContext(r.Context(), "add user", "error", err)
		writeJSON(w, http.StatusInternalServerError, generated.ErrorResponse{Error: "添加邮箱失败"})
	default:
		invitation := generated.InvitationStatusNotRequested
		if sendInvitation {
			invitation = generated.InvitationStatusSent
			if err := s.auth.SendInvitation(r.Context(), user.Email, s.config.InvitationLoginURL); err != nil {
				invitation = generated.InvitationStatusFailed
				slog.ErrorContext(r.Context(), "send user invitation failed", "user_id", user.ID, "request_id", requestID(r), "error", err)
			}
		}
		writeJSON(w, http.StatusCreated, generated.AddUserResponse{
			Id: user.ID, Email: openapi_types.Email(user.Email), Role: generated.AddUserResponseRole(user.Role),
			CreatedAt: user.CreatedAt, Invitation: invitation,
		})
	}
}

// DeleteUser 删除普通用户邮箱并撤销其会话。
func (s *Server) DeleteUser(w http.ResponseWriter, r *http.Request, id string) {
	err := s.auth.DeleteUser(r.Context(), id)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeJSON(w, http.StatusNotFound, generated.ErrorResponse{Error: "用户不存在"})
	case errors.Is(err, auth.ErrCannotDelete):
		writeJSON(w, http.StatusForbidden, generated.ErrorResponse{Error: "不能删除管理员邮箱"})
	case err != nil:
		slog.ErrorContext(r.Context(), "delete user", "error", err)
		writeJSON(w, http.StatusInternalServerError, generated.ErrorResponse{Error: "删除邮箱失败"})
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
