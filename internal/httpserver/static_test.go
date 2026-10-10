package httpserver

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stacknest/server/internal/project"
)

// TestMissingAssetsDoNotBecomeHTML 验证管理页面、本地站点和版本预览保留 SPA，同时正确报告资源缺失。
func TestMissingAssetsDoNotBecomeHTML(t *testing.T) {
	s := newTestServer(t)
	created, err := s.projects.Create(context.Background(), project.CreateInput{OwnerID: "alice", Name: "Routing", Slug: "routing", FileName: "site.zip", Source: bytes.NewReader(staticZip(t))})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(s.config.WebDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.config.WebDir, "index.html"), []byte("<!doctype html><h1>Admin</h1>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.config.SitesDir, "routing", "present.css"), []byte("h1{color:red}"), 0600); err != nil {
		t.Fatal(err)
	}
	version := strings.TrimPrefix(created.Project.Deployments[0].ID, "dep-")
	for _, host := range []string{"localhost", "stack-nest-routing.localhost", "stack-nest--" + version + ".localhost"} {
		for _, path := range []string{"/", "/client/route", "/users/john.smith", "/release.v2/page"} {
			w := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+path, nil))
			if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("SPA route %s%s: %d", host, path, w.Code)
			}
		}
		for _, path := range []string{"/missing.css", "/assets/missing.JS", "/missing.svg", "/missing.json?cache=no", "/missing.woff2", "/missing.html"} {
			for _, method := range []string{"GET", "HEAD"} {
				w := httptest.NewRecorder()
				s.http.Handler.ServeHTTP(w, httptest.NewRequest(method, "http://"+host+path, nil))
				if w.Code != 404 {
					t.Fatalf("missing %s %s%s: %d", method, host, path, w.Code)
				}
			}
		}
	}
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "http://stack-nest-routing.localhost/present.css", nil))
	if w.Code != 200 || w.Body.String() != "h1{color:red}" || !strings.Contains(w.Header().Get("Content-Type"), "text/css") {
		t.Fatal("existing CSS was incorrectly rejected", w.Code)
	}
}
