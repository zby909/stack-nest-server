package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"stacknest/server/internal/project"
)

// exerciseAuthenticatedManagement 使用真实会话和数据库验证新接口的权限与编辑持久化。
func exerciseAuthenticatedManagement(t *testing.T, s *Server, owner, foreign, admin *http.Cookie) {
	t.Helper()
	// call 携带固定同源与用户 Cookie，不绕过认证中间件。
	call := func(method, path, body, match string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "localhost:8080"
		r.Header.Set("Origin", "http://localhost:8080")
		r.Header.Set("Content-Type", "application/json")
		if match != "" {
			r.Header.Set("If-Match", strconv.Quote(match))
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(response, r)
		return response
	}
	var value project.Project
	before := call(http.MethodGet, "/api/projects/ordinary-site", "", "", owner)
	if before.Code != 200 || json.Unmarshal(before.Body.Bytes(), &value) != nil {
		t.Fatalf("owned project: %s", before.Body.String())
	}
	path := "/api/projects/ordinary-site"
	match := value.UpdatedAt.Format(time.RFC3339Nano)
	for _, cookie := range []*http.Cookie{nil, foreign} {
		want := 404
		if cookie == nil {
			want = 401
		}
		if response := call(http.MethodPatch, path, `{"name":"Forbidden","description":""}`, match, cookie); response.Code != want {
			t.Fatalf("edit permission: %d %s", response.Code, response.Body.String())
		}
		if response := call(http.MethodGet, path+"/export?deploymentId="+value.Deployments[0].ID, "", "", cookie); response.Code != want {
			t.Fatalf("export permission: %d %s", response.Code, response.Body.String())
		}
		if response := call(http.MethodGet, path+"/files?deploymentId="+value.Deployments[0].ID, "", "", cookie); response.Code != want {
			t.Fatalf("file list permission: %d %s", response.Code, response.Body.String())
		}
	}
	edited := call(http.MethodPatch, path, `{"name":"Edited by owner","description":"Saved in PostgreSQL"}`, match, owner)
	if edited.Code != 200 {
		t.Fatalf("owner edit: %d %s", edited.Code, edited.Body.String())
	}
	persisted := call(http.MethodGet, path, "", "", admin)
	var updated project.Project
	if json.Unmarshal(persisted.Body.Bytes(), &updated) != nil || updated.Name != "Edited by owner" || updated.Slug != value.Slug || updated.OwnerID != value.OwnerID || updated.Deployments[0].ID != value.Deployments[0].ID {
		t.Fatalf("persisted metadata: %s", persisted.Body.String())
	}
	if got := call(http.MethodPatch, path, `{"name":"Stale","description":""}`, match, admin).Code; got != 409 {
		t.Fatalf("database accepted stale edit: %d", got)
	}
	for _, cookie := range []*http.Cookie{owner, admin} {
		for _, deployment := range updated.Deployments {
			listing := call(http.MethodGet, path+"/files?deploymentId="+deployment.ID, "", "", cookie)
			if listing.Code != 200 || !strings.Contains(listing.Body.String(), "index.html") {
				t.Fatalf("authorized file list: %d %s", listing.Code, listing.Body.String())
			}
			response := call(http.MethodGet, path+"/export?deploymentId="+deployment.ID, "", "", cookie)
			if response.Code != 200 || response.Header().Get("Content-Type") != "application/zip" {
				t.Fatalf("authorized export: %d %s", response.Code, response.Body.String())
			}
		}
	}
	if got := call(http.MethodGet, "/api/backups/status", "", "", owner).Code; got != 403 {
		t.Fatalf("ordinary backup access: %d", got)
	}
	if got := call(http.MethodGet, "/api/backups/status", "", "", admin).Code; got != 200 {
		t.Fatalf("admin backup access: %d", got)
	}
	for _, cookie := range []*http.Cookie{nil, foreign} {
		want := 404
		if cookie == nil {
			want = 401
		}
		if got := call(http.MethodPut, path+"/access", `{"paused":true}`, updated.UpdatedAt.Format(time.RFC3339Nano), cookie).Code; got != want {
			t.Fatalf("pause permission: %d", got)
		}
	}
	for _, pause := range []bool{true, false} {
		body, state, cookie := `{"paused":true}`, "paused", owner
		if !pause {
			body, state, cookie = `{"paused":false}`, "online", admin
		}
		response := call(http.MethodPut, path+"/access", body, updated.UpdatedAt.Format(time.RFC3339Nano), cookie)
		if response.Code != 200 {
			t.Fatalf("database access update: %s", response.Body.String())
		}
		stored := call(http.MethodGet, path, "", "", owner)
		if json.Unmarshal(stored.Body.Bytes(), &updated) != nil || updated.Status != state {
			t.Fatalf("persisted access: %s", stored.Body.String())
		}
	}
}
