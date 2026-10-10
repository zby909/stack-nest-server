package httpserver

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/project"
)

// managementProject 创建真实文件和项目元数据用于编辑、导出验收。
func managementProject(t *testing.T, s *Server) project.Project {
	t.Helper()
	response := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(response, createProjectRequest(t, "Metadata", "metadata"))
	var value project.Project
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || response.Code != 201 {
		t.Fatalf("create: %s %v", response.Body.String(), err)
	}
	return value
}

// metadataRequest 构造严格 JSON 和项目版本请求，支持伪造归属场景。
func metadataRequest(p project.Project, body string, actor *auth.User) *http.Request {
	r := httptest.NewRequest(http.MethodPatch, "/api/projects/"+p.Slug, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("If-Match", strconv.Quote(p.UpdatedAt.Format(time.RFC3339Nano)))
	if actor != nil {
		r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, *actor))
	}
	return r
}

// TestMetadataHTTPFlow 校验严格字段、过期页面和编辑不影响静态页面。
func TestMetadataHTTPFlow(t *testing.T) {
	s := newTestServer(t)
	p := managementProject(t, s)
	for _, body := range []string{`{"name":"New"}`, `{"name":null,"description":""}`, `{"name":"New","description":"","slug":"changed"}`, `{"name":" ","description":""}`, `{"name":"New","description":""} {}`} {
		response := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(response, metadataRequest(p, body, nil))
		if response.Code != 400 {
			t.Fatalf("accepted invalid metadata %s: %d", body, response.Code)
		}
	}
	response := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(response, metadataRequest(p, `{"name":"Changed","description":"Notes"}`, nil))
	var updated project.Project
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &updated) != nil || updated.Name != "Changed" || updated.Slug != p.Slug || updated.Deployments[0].ID != p.Deployments[0].ID {
		t.Fatalf("metadata: %d %s", response.Code, response.Body.String())
	}
	stale := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(stale, metadataRequest(p, `{"name":"Stale","description":""}`, nil))
	if stale.Code != 409 {
		t.Fatalf("stale edit: %d", stale.Code)
	}
	foreign := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(foreign, metadataRequest(updated, `{"name":"Foreign","description":""}`, &auth.User{ID: "other", Role: auth.RoleUser}))
	if foreign.Code != 404 {
		t.Fatalf("foreign edit: %d", foreign.Code)
	}
	preview := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(preview, previewRequest(p.Slug))
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "StackNest Test") {
		t.Fatalf("editing broke static page: %s", preview.Body.String())
	}
}

// TestExportHTTPFlow 验证真实 ZIP、下载头、Range、无权限、并发限制及临时文件清理。
func TestExportHTTPFlow(t *testing.T) {
	s := newTestServer(t)
	p := managementProject(t, s)
	path := "/api/projects/" + p.Slug + "/export?deploymentId=" + p.Deployments[0].ID
	for _, scenario := range []string{"full", "range", "foreign", "busy", "stale"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		want := 200
		switch scenario {
		case "range":
			r.Header.Set("Range", "bytes=0-19")
			want = 206
		case "foreign":
			r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, auth.User{ID: "other", Role: auth.RoleUser}))
			want = 404
		case "busy":
			s.exportMu.Lock()
			want = 429
		case "stale":
			r.URL.RawQuery = "deploymentId=retired"
			want = 409
		}
		response := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(response, r)
		if scenario == "busy" {
			s.exportMu.Unlock()
		}
		if response.Code != want {
			t.Fatalf("%s: %d %s", scenario, response.Code, response.Body.String())
		}
		if scenario == "full" {
			if response.Header().Get("Content-Type") != "application/zip" || response.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment;") {
				t.Fatalf("unsafe download headers: %v", response.Header())
			}
			archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
			if err != nil || len(archive.File) != 1 || archive.File[0].Name != "index.html" {
				t.Fatalf("invalid ZIP: %+v %v", archive, err)
			}
			file, err := archive.File[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(file)
			_ = file.Close()
			if err != nil || !strings.Contains(string(content), "StackNest Test") {
				t.Fatalf("wrong ZIP contents: %q %v", content, err)
			}
		}
		if scenario == "range" && response.Body.Len() != 20 {
			t.Fatalf("wrong range length: %d", response.Body.Len())
		}
		entries, err := os.ReadDir(filepath.Join(s.config.Storage.Dir, "exports"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("temporary ZIP retained: %v %v", entries, err)
		}
	}
}

// TestBackupHTTPRestriction 验证不可读取和普通用户均不会获得备份摘要。
func TestBackupHTTPRestriction(t *testing.T) {
	s := newTestServer(t)
	s.config.BackupStatusFile = filepath.Join(t.TempDir(), "status.json")
	for _, scenario := range []string{"foreign", "missing", "corrupt"} {
		r := httptest.NewRequest(http.MethodGet, "/api/backups/status", nil)
		want := 200
		if scenario == "foreign" {
			r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, auth.User{ID: "other", Role: auth.RoleUser}))
			want = 403
		}
		if scenario == "corrupt" {
			_ = os.WriteFile(s.config.BackupStatusFile, []byte(`{"password":"test-secret"}`), 0o600)
			want = 503
		}
		response := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(response, r)
		if response.Code != want || strings.Contains(response.Body.String(), "test-secret") || strings.Contains(response.Body.String(), s.config.BackupStatusFile) {
			t.Fatalf("backup response leaked data: %d %s", response.Code, response.Body.String())
		}
	}
}

// TestMetadataEscapedInput 验证合法最大字段经 JSON 转义后仍可编辑，字段上限继续执行。
func TestMetadataEscapedInput(t *testing.T) {
	s := newTestServer(t)
	p := managementProject(t, s)
	name, description := strings.Repeat("&", 256), strings.Repeat("<", 4096)
	body, err := json.Marshal(map[string]string{"name": name, "description": description})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 8192 {
		t.Fatal("fixture did not exercise JSON expansion")
	}
	response := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(response, metadataRequest(p, string(body), nil))
	var updated project.Project
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &updated) != nil ||
		updated.Name != name || updated.Description != description ||
		updated.Deployments[0].ID != p.Deployments[0].ID {
		t.Fatalf("escaped metadata rejected: status=%d bytes=%d", response.Code, len(body))
	}
	body, err = json.Marshal(map[string]string{"name": name, "description": description + "<"})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	s.http.Handler.ServeHTTP(response, metadataRequest(updated, string(body), nil))
	if response.Code != 400 {
		t.Fatalf("oversized field accepted: %d", response.Code)
	}
}
