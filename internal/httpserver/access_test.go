package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"stacknest/server/internal/project"
)

// TestSiteAccessHTTP 验证根页和资源都暂停，恢复后原文件继续可用。
func TestSiteAccessHTTP(t *testing.T) {
	s := newTestServer(t)
	value := managementProject(t, s)
	for _, pause := range []bool{true, false} {
		body := `{"paused":true}`
		want := 503
		if !pause {
			body = `{"paused":false}`
			want = 200
		}
		r := metadataRequest(value, body, nil)
		r.Method = http.MethodPut
		r.URL.Path += "/access"
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, r)
		var next project.Project
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &next) != nil {
			t.Fatalf("access: %d %s", w.Code, w.Body.String())
		}
		if next.ID != value.ID || next.Deployments[0].ID != value.Deployments[0].ID {
			t.Fatal("access changed project files")
		}
		value = next
		for _, path := range []string{"/", "/index.html", "/client/route"} {
			expected := want
			if !pause && path == "/index.html" {
				expected = http.StatusMovedPermanently
			}
			preview := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(preview, httptest.NewRequest(http.MethodGet, "http://stack-nest-metadata.localhost:8080"+path, nil))
			if preview.Code != expected || preview.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s: %d", path, preview.Code)
			}
		}
	}
}
