package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"stacknest/server/internal/project"
)

// TestHTMLMultipartUpload 验证直接上传接口保留原文件名、公开入口固定为 index.html。
func TestHTMLMultipartUpload(t *testing.T) {
	server := newTestServer(t)
	content := []byte("<!doctype html><h1>Single HTML</h1>")
	request := createProjectRequestWithNamedArchive(t, "Single page", "single-page", "报告.HTM", content)
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	var value project.Project
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Deployments) != 1 || value.Deployments[0].FileName != "报告.HTM" {
		t.Fatal("original filename lost")
	}
	for _, path := range []string{"/", "/index.html"} {
		response = httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://stack-nest-single-page.localhost:8080"+path, nil))
		if path == "/index.html" {
			if response.Code != http.StatusMovedPermanently || response.Header().Get("Location") != "./" {
				t.Fatal("index.html should redirect to the site root")
			}
			continue
		}
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), content) {
			t.Fatalf("incorrect entry %s: %d", path, response.Code)
		}
	}
}
