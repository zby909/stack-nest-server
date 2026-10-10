package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/resumable"
)

// TestDraftHTTPWorkflow 用真实续传和 HTTP 路由验证独立域名、正式发布及管理源隔离。
func TestDraftHTTPWorkflow(t *testing.T) {
	s := newTestServer(t)
	content := staticZip(t)
	hash := sha256.Sum256(content)
	input := resumable.Request{ID: uuid.NewString(), Kind: "create", Name: "Draft", Slug: "draft-demo", Preview: true, Notes: "首次预览", FileName: "site.zip", Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}
	body, _ := json.Marshal(input)
	if w := resumableRequest(s, "POST", "/api/uploads", body, "alice", 0); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	path := "/api/uploads/" + input.ID
	if w := resumableRequest(s, "PATCH", path, content, "alice", 0); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := resumableRequest(s, "POST", path+"/complete", nil, "alice", 0)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var value generated.Project
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Draft == nil || value.Draft.PreviewUrl == nil || value.Status != "draft" || value.Deployments != nil {
		t.Fatalf("invalid draft response: %s", w.Body.String())
	}
	preview := *value.Draft.PreviewUrl
	for _, target := range []string{preview, preview + "client/route", preview + "index.html"} {
		r := httptest.NewRequest("GET", target, nil)
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
			t.Fatalf("preview: %d %s", w.Code, w.Body.String())
		}
	}
	for _, target := range []string{preview + "api/projects", preview + "sites/draft-demo/", preview + ".env", preview + "../secret", preview + "openapi.yaml", preview + "missing.css", preview + "assets/missing.js", preview + "missing.PNG"} {
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != 404 {
			t.Fatalf("preview escaped boundary: %s %d", target, w.Code)
		}
	}
	body = []byte(`{"draftId":"` + value.Draft.Id + `","expectedId":""}`)
	if w := resumableRequest(s, "POST", "/api/projects/draft-demo/draft/publish", body, "bob", 0); w.Code != 404 {
		t.Fatalf("cross-user publish: %d", w.Code)
	}
	if w := resumableRequest(s, "POST", "/api/projects/draft-demo/draft/publish", body, "alice", 0); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	published, err := s.projects.Get(context.Background(), "draft-demo")
	if err != nil || published.Draft != nil || published.Status != "online" || published.Deployments[0].ID != value.Draft.Id {
		t.Fatalf("publication mismatch: %+v %v", published, err)
	}
	w = httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", preview, nil))
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("html")) {
		t.Fatalf("published preview: %d", w.Code)
	}
	if !strings.HasPrefix(preview, "http://stack-nest--") {
		t.Fatal("preview not separated")
	}
}
