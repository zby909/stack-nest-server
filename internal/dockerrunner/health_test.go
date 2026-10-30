package dockerrunner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestHTTPHealthChecksServiceNotJustPort 验证端口开放但 HTTP 报错时不会视为服务正常。
func TestHTTPHealthChecksServiceNotJustPort(t *testing.T) {
	for _, status := range []int{200, 204, 302, 404, 500, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path != "/health" || req.Method != http.MethodGet {
				t.Errorf("unexpected health request %s %s", req.Method, req.URL.Path)
			}
			w.WriteHeader(status)
		}))
		address := strings.TrimPrefix(server.URL, "http://")
		err := probeAddress(context.Background(), address, "/health")
		if (err == nil) != (status >= 200 && status < 300) {
			t.Fatalf("status %d: %v", status, err)
		}
		if status == 404 && !strings.Contains(err.Error(), "404") {
			t.Fatal("HTTP failure status was hidden", err)
		}
		if err := probeAddress(context.Background(), address, ""); err != nil {
			t.Fatal("compatible TCP check failed", err)
		}
		server.Close()
	}
}

// TestHealthProbeNeverFollowsRedirect 阻止健康端点把高权限运行器重定向到其他服务。
func TestHealthProbeNeverFollowsRedirect(t *testing.T) {
	var visits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		visits.Add(1)
		w.WriteHeader(204)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, target.URL, 302)
	}))
	defer redirect.Close()
	if probeAddress(context.Background(), strings.TrimPrefix(redirect.URL, "http://"), "/health") == nil || visits.Load() != 0 {
		t.Fatal("redirected health request accepted or reached another service")
	}
}

// TestHealthProbeCancellation 验证页面取消请求不会让慢服务持续占用探测。
func TestHealthProbeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if probeAddress(ctx, "127.0.0.1:1", "/health") == nil {
		t.Fatal("cancelled probe accepted")
	}
}
