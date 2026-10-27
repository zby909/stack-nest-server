package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/buildinfo"
	"stacknest/server/internal/doctor"
)

// platformServer 使用真实会话中间件与虚构身份，不连接真实数据库或邮件。
func platformServer(t *testing.T, role string, inspect func(context.Context) doctor.Report) *Server {
	t.Helper()
	actor := auth.User{ID: "actor", Email: "user@example.com", Role: role}
	if role == auth.RoleSuperAdmin {
		actor.Email, actor.Role = "owner@example.com", auth.RoleAdmin
	}
	service, err := auth.NewService(networkAuthStore{actor: actor}, &testMailer{}, "test-secret-at-least-thirty-two-characters")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.SetSuperAdmin(context.Background(), "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	return New(Config{SitesDir: t.TempDir(), Inspect: inspect}, nil, service)
}

// platformRequest 构造有来源与可选 Cookie 的真实管理 API 请求。
func platformRequest(method, path, origin string, loggedIn bool) *http.Request {
	req := httptest.NewRequest(method, "http://localhost"+path, nil)
	req.Header.Set("Origin", origin)
	if loggedIn {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: strings.Repeat("a", 43)})
	}
	return req
}

// TestPlatformRoleBoundary 读取版本限管理员，诊断限超管，匿名和跨站请求不能触发探测。
func TestPlatformRoleBoundary(t *testing.T) {
	for _, role := range []string{auth.RoleUser, auth.RoleAdmin, auth.RoleSuperAdmin} {
		var runs int
		server := platformServer(t, role, func(context.Context) doctor.Report { runs++; return doctor.Report{Checks: []doctor.Check{}} })
		for _, tc := range []struct {
			method, path string
			allowed      bool
		}{
			{"GET", "/api/version", role != auth.RoleUser},
			{"POST", "/api/system/diagnostics", role == auth.RoleSuperAdmin},
		} {
			res := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(res, platformRequest(tc.method, tc.path, "http://localhost", true))
			want := 403
			if tc.allowed {
				want = 200
			}
			if res.Code != want || res.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s %s: %d %s", role, tc.path, res.Code, res.Body.String())
			}
		}
		wantRuns := 0
		if role == auth.RoleSuperAdmin {
			wantRuns = 1
		}
		if runs != wantRuns {
			t.Fatal("unauthorized role reached inspector")
		}
	}
	server := platformServer(t, auth.RoleSuperAdmin, func(context.Context) doctor.Report {
		t.Fatal("unauthorized request reached inspector")
		return doctor.Report{}
	})
	for _, tc := range []struct {
		method, path, origin string
		cookie               bool
		status               int
	}{
		{"GET", "/api/version", "http://localhost", false, 401},
		{"POST", "/api/system/diagnostics", "http://localhost", false, 401},
		{"POST", "/api/system/diagnostics", "https://untrusted.example", true, 403},
	} {
		res := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(res, platformRequest(tc.method, tc.path, tc.origin, tc.cookie))
		if res.Code != tc.status {
			t.Fatalf("wanted %d got %d", tc.status, res.Code)
		}
	}
}

// TestDiagnosticsSerialAndCancellation 验证诊断有截止时间，并发不重复运行，客户端取消后释放入口。
func TestDiagnosticsSerialAndCancellation(t *testing.T) {
	started := make(chan struct{})
	var runs atomic.Int32
	server := platformServer(t, auth.RoleSuperAdmin, func(ctx context.Context) doctor.Report {
		runs.Add(1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			t.Error("missing bounded diagnostic deadline")
		}
		close(started)
		<-ctx.Done()
		return doctor.Report{}
	})
	req := platformRequest("POST", "/api/system/diagnostics", "http://localhost", true)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	first := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() { defer close(finished); server.http.Handler.ServeHTTP(first, req.WithContext(ctx)) }()
	<-started
	second := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(second, req)
	if second.Code != 409 || runs.Load() != 1 {
		t.Fatal("parallel diagnostics were not rejected")
	}
	cancel()
	<-finished
	if first.Code != 504 || !server.diagnosticMu.TryLock() {
		t.Fatal("cancelled diagnostic did not release the lock")
	}
	server.diagnosticMu.Unlock()
}

// TestDiagnosticReportAndBuildIdentity 失败项作为安全报告展示，版本读取真实编译值；缺配置明确不可用。
func TestDiagnosticReportAndBuildIdentity(t *testing.T) {
	server := platformServer(t, auth.RoleSuperAdmin, func(context.Context) doctor.Report {
		return doctor.Report{Failed: true, Checks: []doctor.Check{{Name: "smtp", Status: "fail", Message: "SMTP 认证失败，请检查配置"}}}
	})
	res := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(res, platformRequest("POST", "/api/system/diagnostics", "http://localhost", true))
	var result diagnosticResult
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &result) != nil || !result.Failed || result.CheckedAt.IsZero() || len(result.Checks) != 1 {
		t.Fatal("failure report was not preserved")
	}
	res = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(res, platformRequest("GET", "/api/version", "http://localhost", true))
	var version buildinfo.Info
	if json.Unmarshal(res.Body.Bytes(), &version) != nil || version != buildinfo.Current() {
		t.Fatal("build identity changed")
	}
	server.config.Inspect = nil
	res = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(res, platformRequest("POST", "/api/system/diagnostics", "http://localhost", true))
	if res.Code != 503 {
		t.Fatal("missing inspector accepted")
	}
}
