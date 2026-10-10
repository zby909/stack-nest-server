package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestActivityQueryHTTP 验证真正路由中的参数绑定和错误响应，避免无效查询被默默忽略。
func TestActivityQueryHTTP(t *testing.T) {
	s := newTestServer(t)
	for _, query := range []string{"page=0", "pageSize=101", "page=abc", "from=not-a-date", "action=unknown", "from=2026-10-08T00:00:00Z&to=2026-10-07T00:00:00Z"} {
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/activity?"+query, nil))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d", query, w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/activity?page=1&pageSize=20", nil))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("valid query: %d", w.Code)
	}
}
