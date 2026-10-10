package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stacknest/server/internal/auth"
)

// exerciseHTTPSCookies 用真实数据库会话验证 HTTPS 登录、旧名称拒绝及退出撤销。
func exerciseHTTPSCookies(t *testing.T, server *Server, service *auth.Service, mailer *testMailer) {
	t.Helper()
	if err := service.RequestCode(context.Background(), "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://localhost"+path, bytes.NewBufferString(body))
		r.Header.Set("Origin", "https://localhost")
		r.Header.Set("Content-Type", "application/json")
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(w, r)
		return w
	}
	w := call(http.MethodPost, "/api/auth/verify", `{"email":"admin@example.com","code":"`+mailer.codes["admin@example.com"]+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTPS login: %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "__Host-stacknest_session" || !cookies[0].Secure {
		t.Fatal("HTTPS login cookie is not host protected")
	}
	protected := cookies[0]
	legacy := &http.Cookie{Name: sessionCookie, Value: protected.Value}
	if w := call(http.MethodGet, "/api/auth/session", "", legacy); w.Code != http.StatusUnauthorized {
		t.Fatalf("accepted legacy HTTPS cookie: %d", w.Code)
	}
	if w := call(http.MethodGet, "/api/auth/session", "", legacy, protected); w.Code != http.StatusOK {
		t.Fatalf("protected session failed: %d", w.Code)
	}
	if w := call(http.MethodPost, "/api/auth/logout", "", legacy, protected); w.Code != http.StatusNoContent {
		t.Fatalf("HTTPS logout failed: %d", w.Code)
	}
	if w := call(http.MethodGet, "/api/auth/session", "", protected); w.Code != http.StatusUnauthorized {
		t.Fatalf("HTTPS session survived logout: %d", w.Code)
	}
}

type cookieStore struct {
	auth.Store
	readHash, deletedHash string
}

// FindSession 记录实际查询的令牌摘要，检查同名旧 Cookie 不会抢占 HTTPS 会话。
func (s *cookieStore) FindSession(_ context.Context, hash string, _ time.Time) (auth.User, error) {
	s.readHash = hash
	return auth.User{ID: "owner", Email: "owner@example.com", Role: auth.RoleUser}, nil
}

// DeleteSession 记录实际撤销的会话，避免注销时删除错误 Cookie 对应的令牌。
func (s *cookieStore) DeleteSession(_ context.Context, hash string) error {
	s.deletedHash = hash
	return nil
}

// TestSessionCookieScope 检查 TLS 和受代理保护的 HTTPS 使用浏览器强制的主机隔离。
func TestSessionCookieScope(t *testing.T) {
	for _, tc := range []struct{ name, url, forwarded, cookie string }{
		{"local HTTP", "http://localhost:18080/", "", "stacknest_session"},
		{"TLS", "https://stack-nest.example.com/", "", "__Host-stacknest_session"},
		{"HTTPS proxy", "http://stack-nest.example.com/", "https", "__Host-stacknest_session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.url, nil)
			r.Header.Set("X-Forwarded-Proto", tc.forwarded)
			for _, age := range []int{3600, -1} {
				w := httptest.NewRecorder()
				setSessionCookie(w, r, "test-token", age)
				cookies := w.Result().Cookies()
				if len(cookies) != 1 {
					t.Fatalf("cookie count = %d", len(cookies))
				}
				cookie := cookies[0]
				if cookie.Name != tc.cookie || cookie.Domain != "" || cookie.Path != "/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != age || cookie.Secure != strings.HasPrefix(tc.cookie, "__Host-") {
					t.Fatalf("unexpected cookie scope: %+v", cookie)
				}
			}
		})
	}
}

// TestHTTPSRejectsLegacyCookie 同时覆盖读取与注销，HTTPS 不回退接受可被兄弟域名设置的旧名称。
func TestHTTPSRejectsLegacyCookie(t *testing.T) {
	for _, includeProtected := range []bool{false, true} {
		store := &cookieStore{}
		service, err := auth.NewService(store, &testMailer{}, "test-secret-at-least-thirty-two-bytes")
		if err != nil {
			t.Fatal(err)
		}
		server := &Server{auth: service}
		r := httptest.NewRequest(http.MethodGet, "https://stack-nest.example.com/api/auth/session", nil)
		r.AddCookie(&http.Cookie{Name: "stacknest_session", Value: strings.Repeat("a", 43)})
		protected := strings.Repeat("b", 43)
		if includeProtected {
			r.AddCookie(&http.Cookie{Name: "__Host-stacknest_session", Value: protected})
		}
		w := httptest.NewRecorder()
		server.GetAuthSession(w, r)
		wantCode, wantHash := http.StatusUnauthorized, ""
		if includeProtected {
			wantCode, wantHash = http.StatusOK, auth.TokenHash(protected)
		}
		if w.Code != wantCode || store.readHash != wantHash {
			t.Fatalf("protected=%v status=%d read unexpected session=%v", includeProtected, w.Code, store.readHash != wantHash)
		}
		server.Logout(httptest.NewRecorder(), r)
		if store.deletedHash != wantHash {
			t.Fatal("logout revoked the legacy injected session")
		}
	}
}
