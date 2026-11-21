package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stacknest/server/internal/deployclient"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/storage"
)

// TestEmptyProjectAdmission 验证只登记元数据可在上传暂停时进行，且不能借此指定归属或伪造版本。
func TestEmptyProjectAdmission(t *testing.T) {
	s := newTestServer(t)
	s.config.Storage.Read = func(string) (storage.Usage, error) {
		t.Fatal("empty project checked the file upload budget")
		return storage.Usage{}, nil
	}
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"name":"  My site  "}`, 201},
		{`{"name":"My site"}`, 409},
		{`{"name":"   "}`, 400},
		{`{"name":"test","slug":"invalid/slug"}`, 400},
		{`{"name":"test","ownerId":"someone-else"}`, 400},
		{`{"name":"test","file":"index.html"}`, 400},
		{`{"name":"test"} {}`, 400},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("create %s: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	p, err := s.projects.Get(t.Context(), "my-site")
	if err != nil || p.Name != "My site" || p.Status != "draft" || p.Draft != nil || len(p.Deployments) != 0 {
		t.Fatalf("unexpected empty project: %+v %v", p, err)
	}
	if _, err := os.Stat(filepath.Join(s.config.SitesDir, p.Slug)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected public site directory: %v", err)
	}
}

// checkEmptyProjectDeployment 使用普通成员、真实项目 Token 和 CLI 客户端验证首版从零到上线。
func checkEmptyProjectDeployment(t *testing.T, s *Server, call func(string, string, string, *http.Cookie) *httptest.ResponseRecorder, owner, other *http.Cookie) {
	t.Helper()
	const slug = "ai-starter"
	const projectPath = "/api/projects/" + slug
	const body = `{"name":"AI Starter","description":"first release from AI"}`
	if w := call("POST", "/api/projects", body, nil); w.Code != 401 {
		t.Fatalf("anonymous create: %d", w.Code)
	}
	w := call("POST", "/api/projects", body, owner)
	var created generated.Project
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.Slug != slug || created.Status != "draft" || created.Draft != nil || created.Deployments != nil {
		t.Fatalf("empty project: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", projectPath+"/deploy-tokens", `{"name":"AI"}`, other); w.Code != 404 {
		t.Fatalf("another member could create a token: %d", w.Code)
	}
	var token struct{ Secret string }
	w = call("POST", projectPath+"/deploy-tokens", `{"name":"AI"}`, owner)
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &token) != nil || token.Secret == "" {
		t.Fatalf("preview token: %d", w.Code)
	}
	httpServer := httptest.NewServer(s.http.Handler)
	defer httpServer.Close()
	client, err := deployclient.New(httpServer.URL, token.Secret)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "first.html")
	const content = "<!doctype html><h1>First AI upload</h1>"
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := client.Upload(t.Context(), file, filepath.Join(dir, "state.json"), "first preview", nil)
	if err != nil || result.ProjectID != created.Id || result.ExpectedID != "" || result.PreviewURL == "" || result.Status != "preview" {
		t.Fatalf("first CLI upload: %+v %v", result, err)
	}
	preview := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(preview, httptest.NewRequest("GET", result.PreviewURL, nil))
	if preview.Code != 200 || preview.Body.String() != content {
		t.Fatalf("first preview: %d", preview.Code)
	}
	official := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(official, previewRequest(slug))
	if official.Code != 503 {
		t.Fatalf("draft available on the public site: %d", official.Code)
	}
	if _, err := client.Publish(t.Context(), result.DraftID, ""); err == nil {
		t.Fatal("preview-only token published")
	}
	publish, _ := json.Marshal(map[string]string{"draftId": result.DraftID, "expectedId": ""})
	if w := call("POST", projectPath+"/draft/publish", string(publish), owner); w.Code != 200 {
		t.Fatalf("first publish: %d %s", w.Code, w.Body.String())
	}
	official = httptest.NewRecorder()
	s.http.Handler.ServeHTTP(official, previewRequest(slug))
	if official.Code != 200 || official.Body.String() != content {
		t.Fatalf("published site: %d", official.Code)
	}
	published, err := client.Project(t.Context())
	if err != nil || published.ID != created.Id || len(published.Deployments) != 1 || published.Deployments[0].ID != result.DraftID || published.Draft != nil {
		t.Fatalf("published project: %+v %v", published, err)
	}
}
