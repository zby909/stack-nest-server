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

// TestStaticHiddenPaths 拒绝本地预览和版本预览的隐藏文件，同时保留普通含点路径。
func TestStaticHiddenPaths(t *testing.T) {
	s := newTestServer(t)
	created, err := s.projects.Create(context.Background(), project.CreateInput{OwnerID: "alice", Name: "Hidden", Slug: "hidden", FileName: "site.zip", Source: bytes.NewReader(staticZip(t))})
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.config.SitesDir, "hidden")
	for _, name := range []string{".env", ".git/config", "assets/.private/key.txt", "assets/app.v1.js"} {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("fixture-secret"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	hosts := []string{"stack-nest-hidden.localhost", "stack-nest--" + strings.TrimPrefix(created.Project.Deployments[0].ID, "dep-") + ".localhost"}
	for _, host := range hosts {
		for _, method := range []string{"GET", "HEAD"} {
			for _, path := range []string{"/.env", "/%2eenv", "/.git/config", "/assets/.private/key.txt"} {
				w := httptest.NewRecorder()
				s.http.Handler.ServeHTTP(w, httptest.NewRequest(method, "http://"+host+path, nil))
				if w.Code != 404 || strings.Contains(w.Body.String(), "fixture-secret") {
					t.Errorf("%s %s%s: hidden file accessible, status=%d", method, host, path, w.Code)
				}
			}
		}
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "http://"+host+"/assets/app.v1.js", nil))
		if w.Code != 200 || w.Body.String() != "fixture-secret" {
			t.Fatal("ordinary dotted resource rejected", host, w.Code)
		}
	}
}
