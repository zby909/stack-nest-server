package deployclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stacknest/server/internal/resumable"
)

// TestRedirectAndURL 确保 URL 校验和重定向不能将部署凭据送到第二个来源。
func TestRedirectAndURL(t *testing.T) {
	for _, url := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/a", "https://example.com?token=x", "https://example.com#x"} {
		if _, err := New(url, "secret"); err == nil {
			t.Fatal("unsafe URL accepted", url)
		}
	}
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	c, _ := New(source.URL, "secret")
	if _, err := c.Project(context.Background()); err == nil || hits != 0 {
		t.Fatal("followed redirect", err, hits)
	}
}

// TestChunkFailuresBounded 持续错误最多尝试四次，已确认进度不会凭空增长。
func TestChunkFailuresBounded(t *testing.T) {
	for _, code := range []int{401, 422, 507, 503, 409} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			patches := 0
			r := resumable.Request{ID: "upload-id", Size: 600 << 10}
			initial := Status{ID: r.ID, Size: r.Size, ChunkSize: 4 << 20, MinChunkSize: 64 << 10}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "PATCH" {
					patches++
					w.WriteHeader(code)
					io.WriteString(w, `{"error":"failed"}`)
					return
				}
				json.NewEncoder(w).Encode(initial)
			}))
			defer server.Close()
			c, _ := New(server.URL, "secret")
			c.Wait = func(context.Context, time.Duration) error { return nil }
			path := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(path, []byte(strings.Repeat("x", 600<<10)), 0o600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			err = c.sendChunks(context.Background(), f, r, &initial, nil)
			var api *APIError
			want := 1
			if code == 503 || code == 409 {
				want = 4
			}
			if !errors.As(err, &api) || patches != want || initial.Offset != 0 {
				t.Fatal("unbounded retries or changed progress", patches, initial.Offset, err)
			}
		})
	}
}

// TestLostChunkResponse 在服务端已提交但响应丢失时查询偏移，不能重复发送已提交字节。
func TestLostChunkResponse(t *testing.T) {
	requests := 0
	r := resumable.Request{ID: "upload-id", Size: 90 << 10}
	status := Status{ID: r.ID, Size: r.Size, ChunkSize: 4 << 20, MinChunkSize: 64 << 10}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "PATCH" {
			requests++
			status.Offset = status.Size
			w.WriteHeader(502)
			return
		}
		json.NewEncoder(w).Encode(status)
	}))
	defer server.Close()
	c, _ := New(server.URL, "secret")
	c.Wait = func(context.Context, time.Duration) error { return nil }
	path := filepath.Join(t.TempDir(), "file")
	os.WriteFile(path, []byte(strings.Repeat("x", int(r.Size))), 0o600)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	initial := status
	if err = c.sendChunks(context.Background(), f, r, &initial, nil); err != nil || requests != 1 || initial.Offset != r.Size {
		t.Fatal("lost response recovery failed", requests, err)
	}
}
