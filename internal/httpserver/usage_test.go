package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/generated"
)

// TestProjectUsageResponse 验证创建与列表携带文件统计，缺失文件不会阻断管理入口。
func TestProjectUsageResponse(t *testing.T) {
	s := newTestServer(t)
	created := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(created, createProjectRequest(t, "Usage", "usage"))
	var value generated.Project
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &value) != nil {
		t.Fatalf("create: %s", created.Body.String())
	}
	if value.Deployments == nil || (*value.Deployments)[0].Files == nil {
		t.Fatal("create response missing usage")
	}
	files := (*value.Deployments)[0].Files
	if files.Bytes != int64(len("<!doctype html><h1>StackNest Test</h1>")) || files.Count != 1 {
		t.Fatalf("wrong usage: %+v", files)
	}
	for _, missing := range []bool{false, true} {
		if missing {
			if err := os.Remove(filepath.Join(s.config.SitesDir, "usage", "index.html")); err != nil {
				t.Fatal(err)
			}
		}
		response := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
		var list generated.ProjectListResponse
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Projects) != 1 {
			t.Fatalf("list: %d %s", response.Code, response.Body.String())
		}
		got := (*list.Projects[0].Deployments)[0].Files
		if missing && got != nil || !missing && (got == nil || *got != *files) {
			t.Fatalf("missing=%v usage=%+v", missing, got)
		}
	}
}
