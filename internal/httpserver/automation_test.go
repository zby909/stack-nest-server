package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/deployclient"
	"stacknest/server/internal/deploytoken"
	"stacknest/server/internal/project"
	"stacknest/server/internal/resumable"
)

type testTokens struct{ grants map[string]deploytoken.Grant }

// CreateDeployToken 为路由测试提供最小存储替身；实际事务另用 PostgreSQL 集成测试覆盖。
func (s *testTokens) CreateDeployToken(context.Context, deploytoken.Token, auth.User) error {
	return nil
}

// ListDeployTokens 返回不含凭据原文的模拟列表。
func (s *testTokens) ListDeployTokens(context.Context, string, auth.User) ([]deploytoken.Token, error) {
	return []deploytoken.Token{}, nil
}

// DeleteDeployToken 仅用于满足管理接口测试所需契约。
func (s *testTokens) DeleteDeployToken(context.Context, string, string, auth.User) error { return nil }

// AuthenticateDeployToken 模拟每个请求重新校验，不缓存已撤销凭据。
func (s *testTokens) AuthenticateDeployToken(_ context.Context, hash string) (deploytoken.Grant, error) {
	g, ok := s.grants[hash]
	if !ok {
		return g, deploytoken.ErrInvalid
	}
	return g, nil
}

// automationServer 使用真实项目文件与业务服务，只替换身份存储。
func automationServer(t *testing.T) (*Server, *testTokens, deploytoken.Token, string, project.Project) {
	t.Helper()
	s := newTestServer(t)
	created, err := s.projects.Create(context.Background(), project.CreateInput{OwnerID: "alice", OwnerEmail: "alice@example.com", Name: "Demo", Slug: "demo", FileName: "a.html", Source: strings.NewReader("<p>old</p>")})
	if err != nil {
		t.Fatal(err)
	}
	token, plain, err := deploytoken.New(created.Project.ID, "local AI", 30, true)
	if err != nil {
		t.Fatal(err)
	}
	token.OwnerID = "alice"
	fake := &testTokens{grants: map[string]deploytoken.Grant{token.Hash: {Token: token, Slug: "demo", User: auth.User{ID: "alice", Email: "alice@example.com", Role: auth.RoleUser}}}}
	s.config.DeployTokens = fake
	return s, fake, token, plain, created.Project
}

// deployRequest 经真实 HTTP 边界发送 Bearer 请求，刻意不携带 Cookie 或 Origin。
func deployRequest(s *Server, method, path, plain string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Host = "localhost:8080"
	r.Header.Set("Authorization", "Bearer "+plain)
	r.Header.Set("Content-Type", "application/json")
	if method == "PATCH" {
		r.Header.Set("Content-Type", "application/octet-stream")
		r.Header.Set("Upload-Offset", "0")
	}
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, r)
	return w
}

// TestAutomationBoundary 防止 Token 跨项目、直接上线、接管浏览器上传或绕过正常认证。
func TestAutomationBoundary(t *testing.T) {
	s, fake, token, plain, p := automationServer(t)
	content := []byte("<p>new</p>")
	sum := sha256.Sum256(content)
	input := resumable.Request{ID: uuid.NewString(), Kind: "update", Slug: p.Slug, ExpectedID: project.CurrentID(p), FileName: "guide.html", Preview: true, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
	for _, change := range []func(*resumable.Request){func(v *resumable.Request) { v.Preview = false }, func(v *resumable.Request) { v.Slug = "other" }, func(v *resumable.Request) { v.Kind = "create"; v.ExpectedID = ""; v.Name = "new" }} {
		v := input
		change(&v)
		body, _ := json.Marshal(v)
		if w := deployRequest(s, "POST", deployPrefix+"uploads", plain, body); w.Code != 403 {
			t.Fatalf("scope escaped: %d %s", w.Code, w.Body.String())
		}
	}
	body, _ := json.Marshal(input)
	if w := deployRequest(s, "POST", deployPrefix+"uploads", plain, body); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := resumableRequest(s, "GET", "/api/uploads/"+input.ID, nil, "alice", 0); w.Code != 404 {
		t.Fatal("browser accessed token upload", w.Code)
	}
	other, second, _ := deploytoken.New(p.ID, "second", 30, true)
	other.OwnerID = "alice"
	fake.grants[other.Hash] = deploytoken.Grant{Token: other, Slug: "demo", User: fake.grants[token.Hash].User}
	if w := deployRequest(s, "GET", deployPrefix+"uploads/"+input.ID, second, nil); w.Code != 404 {
		t.Fatal("token accessed another upload", w.Code)
	}
	if w := deployRequest(s, "PATCH", deployPrefix+"uploads/"+input.ID, plain, content); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for range 2 {
		if w := deployRequest(s, "POST", deployPrefix+"uploads/"+input.ID+"/complete", plain, []byte("{}")); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	updated, _ := s.projects.Get(context.Background(), "demo")
	if project.CurrentID(updated) != project.CurrentID(p) || updated.Draft == nil {
		t.Fatal("upload changed production")
	}
	for _, path := range []string{deployPrefix + "users", deployPrefix + "projects/other", deployPrefix + "apps"} {
		if w := deployRequest(s, "POST", path, plain, []byte("{}")); w.Code != 404 {
			t.Fatal("unexpected automation route", path, w.Code)
		}
	}
	g := fake.grants[token.Hash]
	g.CanPublish = false
	fake.grants[token.Hash] = g
	publish, _ := json.Marshal(map[string]string{"draftId": updated.Draft.ID, "expectedId": project.CurrentID(p)})
	if w := deployRequest(s, "POST", deployPrefix+"publish", plain, publish); w.Code != 403 {
		t.Fatal("preview token published", w.Code)
	}
	delete(fake.grants, token.Hash)
	if w := deployRequest(s, "GET", deployPrefix+"project", plain, nil); w.Code != 401 {
		t.Fatal("revocation ignored", w.Code)
	}
	// 即使 Bearer 有效，普通接口仍需 Cookie / 同源校验。
	s.auth = &auth.Service{}
	protected := s.automationBoundary(s.protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, "/api/users", nil)
		r.Header.Set("Authorization", "Bearer "+second)
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, r)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("bearer replaced cookie auth", w.Code)
		}
	}
}

// TestDeployCLIEndToEnd 用真正的 HTTP、分片、哈希、草稿和发布事务验证客户端，不依赖 Docker。
func TestDeployCLIEndToEnd(t *testing.T) {
	s, _, _, plain, original := automationServer(t)
	server := httptest.NewServer(s.http.Handler)
	defer server.Close()
	client, err := deployclient.New(server.URL, plain)
	if err != nil {
		t.Fatal(err)
	}
	client.Wait = func(context.Context, time.Duration) error { return nil }
	dir := t.TempDir()
	file := filepath.Join(dir, "旅行.html")
	state := filepath.Join(dir, "state.json")
	content := []byte("<html>" + strings.Repeat("travel", 100000) + "</html>")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := client.Upload(context.Background(), file, state, "更新行程", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "preview" || result.PreviewURL == "" || result.ExpectedID != project.CurrentID(original) {
		t.Fatal("wrong preview receipt", result)
	}
	again, err := client.Upload(context.Background(), file, state, "更新行程", nil)
	if err != nil || again.DraftID != result.DraftID {
		t.Fatal("retry not idempotent", err)
	}
	if _, err := client.Publish(context.Background(), result.DraftID, "stale"); err == nil {
		t.Fatal("published on stale baseline")
	}
	for range 2 {
		if value, err := client.Publish(context.Background(), result.DraftID, result.ExpectedID); err != nil || value.Status != "published" {
			t.Fatal("publish or repeat failed", err)
		}
	}
	if _, err := client.Upload(context.Background(), file, state, "different notes", nil); err == nil {
		t.Fatal("resumed different input")
	}
	value, _ := s.projects.Get(context.Background(), "demo")
	if project.CurrentID(value) != result.DraftID || len(value.Deployments) != 2 || value.Draft != nil {
		t.Fatal("revision state incorrect")
	}
	// 同名项目在旧授权检查之后被替换，业务锁内的项目 ID 仍必须阻止发布。
	_, err = s.projects.PublishDraftAs(context.Background(), "demo", project.Actor{ID: "alice", ProjectID: "replacement"}, result.DraftID, result.ExpectedID)
	if !errors.Is(err, project.ErrNotFound) {
		t.Fatal("immutable binding ignored", err)
	}
}
