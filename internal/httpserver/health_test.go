package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/postgres"
	"stacknest/server/internal/storage"
)

// healthRequest 通过真实路由检查公开健康状态与禁止缓存响应。
func healthRequest(t *testing.T, server *Server, want int, status string) {
	t.Helper()
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if response.Code != want || strings.TrimSpace(response.Body.String()) != `{"status":"`+status+`"}` {
		t.Fatalf("health status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("health result can be cached")
	}
}

// TestHealthReportsDependencyFailure 验证未配置、连接失败和恢复均正确报告，且不泄露内部错误。
func TestHealthReportsDependencyFailure(t *testing.T) {
	s := newTestServer(t)
	healthRequest(t, s, http.StatusServiceUnavailable, "unavailable")
	calls := 0
	cause := error(nil)
	s.config.CheckDatabase = func(ctx context.Context) error {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > databaseHealthTimeout || time.Until(deadline) <= 0 {
			t.Fatal("database check lacks bounded context")
		}
		return cause
	}
	healthRequest(t, s, http.StatusOK, "ok")
	cause = errors.New("database connection failed: private-host:5432 private-user")
	healthRequest(t, s, http.StatusServiceUnavailable, "unavailable")
	cause = nil
	healthRequest(t, s, http.StatusOK, "ok")
	if calls != 3 {
		t.Fatal("health did not probe database for each request")
	}
}

// TestHealthTimeout 验证卡住的数据库探测在截止时间结束，而非一直显示正常或挂起。
func TestHealthTimeout(t *testing.T) {
	s := newTestServer(t)
	s.config.CheckDatabase = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	healthRequest(t, s, http.StatusServiceUnavailable, "unavailable")
	if elapsed := time.Since(start); elapsed < databaseHealthTimeout || elapsed > databaseHealthTimeout+time.Second {
		t.Fatalf("unexpected check duration: %v", elapsed)
	}
}

// TestHealthPostgresIntegration 验证实际 PostgreSQL 连接和连接池关闭后的失败状态。
func TestHealthPostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("STACKNEST_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("STACKNEST_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := newTestServer(t)
	s.config.CheckDatabase = store.Ping
	healthRequest(t, s, http.StatusOK, "ok")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	healthRequest(t, s, http.StatusServiceUnavailable, "unavailable")
}

// TestUploadPauseReasonPrivacy 验证容量不足和探测失败的提示明确，但普通用户看不到磁盘详情。
func TestUploadPauseReasonPrivacy(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		for _, role := range []string{auth.RoleAdmin, auth.RoleUser} {
			t.Run(role+map[bool]string{false: "/full", true: "/unavailable"}[unavailable], func(t *testing.T) {
				s := newTestServer(t)
				s.config.Storage.Read = func(string) (storage.Usage, error) {
					if unavailable {
						return storage.Usage{}, errors.New("private /srv/secret-volume details")
					}
					return storage.Usage{TotalBytes: 1000, FreeBytes: 100}, nil
				}
				request := httptest.NewRequest(http.MethodGet, "/api/storage", nil)
				request = request.WithContext(context.WithValue(request.Context(), authContextKey{}, auth.User{ID: "user", Role: role}))
				response := httptest.NewRecorder()
				s.http.Handler.ServeHTTP(response, request)
				want := http.StatusOK
				if unavailable {
					want = http.StatusServiceUnavailable
				}
				body := response.Body.String()
				if response.Code != want || !strings.Contains(body, "已暂停上传和更新") || strings.Contains(body, "secret-volume") {
					t.Fatalf("unexpected pause response: %d %s", response.Code, body)
				}
				if role == auth.RoleUser {
					for _, detail := range []string{"totalBytes", "freeBytes", "usedPercent", "reservedUploadBytes", "90%"} {
						if strings.Contains(body, detail) {
							t.Fatalf("ordinary user saw disk detail: %s", body)
						}
					}
					if !unavailable && !strings.Contains(body, "存储空间不足") {
						t.Fatal("pause reason missing")
					}
				}
			})
		}
	}
}
