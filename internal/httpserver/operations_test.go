package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/activity"
	"stacknest/server/internal/storage"
)

type unreadBody struct{ reads int }

// Read 记录是否在拒绝上传前消耗了请求体。
func (b *unreadBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }

// TestDiskAdmissionBeforeBody 验证磁盘阈值和上传占用检查发生在接收请求体之前。
func TestDiskAdmissionBeforeBody(t *testing.T) {
	for _, busy := range []bool{false, true} {
		server := newTestServer(t)
		server.config.Storage.Read = func(string) (storage.Usage, error) { return storage.Usage{TotalBytes: 1000, FreeBytes: 100}, nil }
		if busy {
			server.uploadMu.Lock()
		}
		body := &unreadBody{}
		request := httptest.NewRequest(http.MethodPost, "/api/projects", body)
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		expected := http.StatusInsufficientStorage
		if busy {
			expected = http.StatusTooManyRequests
			server.uploadMu.Unlock()
		}
		if response.Code != expected || body.reads != 0 {
			t.Fatalf("admission: status=%d bodyReads=%d", response.Code, body.reads)
		}
	}
}

// TestPreviewOriginIsolation 验证后台不能执行上传内容，且预览来源不能访问任何管理 API。
func TestPreviewOriginIsolation(t *testing.T) {
	server := newTestServer(t)
	created := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(created, createProjectRequest(t, "Preview", "preview"))
	if created.Code != http.StatusCreated {
		t.Fatal(created.Body.String())
	}
	for _, target := range []string{
		"http://localhost:8080/sites/preview/",
		"http://stack-nest-preview.localhost:8080/api/users",
		"http://stack-nest-preview.localhost:8080/api/auth/session",
		"http://stack-nest-preview.localhost:8080/api/projects",
		"http://stack-nest-other.localhost:8080/sites/preview/",
	} {
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("isolation failed for %s: %d", target, response.Code)
		}
	}
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, previewRequest("preview"))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("preview unavailable: %d", response.Code)
	}
	if err := os.WriteFile(filepath.Join(server.config.SitesDir, "preview", "main.js"), []byte("console.log('preview')"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/client/route", "/main.js"} {
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://stack-nest-preview.localhost:8080"+path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("root preview unavailable at %s: %d", path, response.Code)
		}
		if path == "/main.js" && response.Body.String() != "console.log('preview')" {
			t.Fatalf("absolute resource did not resolve: %s", response.Body.String())
		}
	}
}

// TestOperationLogKeepsDeleteAndFailure 验证业务日志具有实际文件大小、失败原因，并独立于项目存在。
func TestOperationLogKeepsDeleteAndFailure(t *testing.T) {
	server := newTestServer(t)
	log, err := activity.New(server.config.Storage.Dir)
	if err != nil {
		t.Fatal(err)
	}
	server.config.Activity = log
	created := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(created, createProjectRequest(t, "Logs", "logs"))
	deleted := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/api/projects/logs", nil))
	failed := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(failed, createProjectRequestWithArchive(t, "Invalid", "invalid", []byte("not a zip")))
	listed := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/activity", nil))
	var response struct {
		Events []activity.Event `json:"events"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Events) != 3 {
		t.Fatalf("missing activity: %s", listed.Body.String())
	}
	if response.Events[0].Status != "failed" || response.Events[1].Action != "delete" || response.Events[2].ExpandedBytes != int64(len("<!doctype html><h1>StackNest Test</h1>")) {
		t.Fatalf("unexpected log: %+v", response.Events)
	}
	if response.Events[0].ID != failed.Header().Get("X-Request-ID") {
		t.Fatal("request correlation lost")
	}
}
