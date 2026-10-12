package httpserver

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"stacknest/server/internal/project"
)

// TestVersionPreviewHostBoundary 实际版本只允许一个标签和受信任后缀，其他主机不能读取版本文件。
func TestVersionPreviewHostBoundary(t *testing.T) {
	server := newTestServer(t)
	server.config.BaseDomain = "example.com"
	created, err := server.projects.Create(context.Background(), project.CreateInput{
		OwnerID: "alice", Name: "Host boundary", Slug: "host-boundary",
		FileName: "site.zip", Source: bytes.NewReader(staticZip(t)),
	})
	if err != nil {
		t.Fatal(err)
	}
	label := "stack-nest--" + strings.TrimPrefix(created.Project.Deployments[0].ID, "dep-")
	for _, value := range []struct {
		host   string
		status int
	}{
		{label + ".localhost", http.StatusOK},
		{label + ".localhost:18080", http.StatusOK},
		{strings.ToUpper(label) + ".EXAMPLE.COM", http.StatusOK},
		{label + ".example.com", http.StatusOK},
		{label + ".other.example.com", http.StatusNotFound},
		{label + ".other.localhost", http.StatusNotFound},
		{label + ".example.com.evil", http.StatusNotFound},
		{label + ".invalid", http.StatusNotFound},
		{label + ".", http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest("GET", "http://"+value.host+"/", nil)
		server.http.Handler.ServeHTTP(response, request)
		if response.Code != value.status {
			t.Errorf("host %s: got %d, expected %d", value.host, response.Code, value.status)
		}
	}
}
