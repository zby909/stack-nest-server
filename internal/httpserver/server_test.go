package httpserver

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stacknest/server/internal/generated"
	"stacknest/server/internal/project"
	"stacknest/server/internal/staticdeploy"
)

type memoryStore struct {
	projects map[string]project.Project
}

// UpdateMetadata 模拟不可覆盖的归属和更新时间校验。
func (s *memoryStore) UpdateMetadata(_ context.Context, value project.Project, expectedAt time.Time) error {
	old, ok := s.projects[value.Slug]
	if !ok || old.OwnerID != value.OwnerID || !old.UpdatedAt.Equal(expectedAt) {
		return project.ErrVersionConflict
	}
	s.projects[value.Slug] = value
	return nil
}

// ReplaceDeployment 模拟部署事务，保留与文件一致的最近两版。
func (s *memoryStore) ReplaceDeployment(_ context.Context, slug, ownerID, expectedID string, next project.Deployment) error {
	value, ok := s.projects[slug]
	if !ok || ownerID != "" && value.OwnerID != ownerID {
		return project.ErrNotFound
	}
	if project.CurrentID(value) != expectedID {
		return project.ErrVersionConflict
	}
	if len(value.Deployments) > 0 {
		value.Deployments = []project.Deployment{next, value.Deployments[0]}
	} else {
		value.Deployments = []project.Deployment{next}
	}
	if value.Draft != nil && value.Draft.ID == next.ID {
		value.Draft = nil
	}
	if value.Status == "draft" {
		value.Status = "online"
	}
	value.UpdatedAt = next.CreatedAt
	s.projects[slug] = value
	return nil
}

func newMemoryStore() *memoryStore {
	return &memoryStore{projects: make(map[string]project.Project)}
}

func (s *memoryStore) List(context.Context) ([]project.Project, error) {
	values := make([]project.Project, 0, len(s.projects))
	for _, value := range s.projects {
		values = append(values, value)
	}
	return values, nil
}

func (s *memoryStore) Get(_ context.Context, slug string) (project.Project, error) {
	value, ok := s.projects[slug]
	if !ok {
		return project.Project{}, project.ErrNotFound
	}
	return value, nil
}

func (s *memoryStore) Create(_ context.Context, value project.Project) error {
	if _, ok := s.projects[value.Slug]; ok {
		return project.ErrAlreadyExists
	}
	s.projects[value.Slug] = value
	return nil
}

func (s *memoryStore) Delete(_ context.Context, slug, ownerID string) error {
	value, ok := s.projects[slug]
	if !ok || ownerID != "" && value.OwnerID != ownerID {
		return project.ErrNotFound
	}
	delete(s.projects, slug)
	return nil
}

func newTestServer(t *testing.T) *Server {
	t.Helper()

	dataDir := t.TempDir()
	store := newMemoryStore()

	deployer := staticdeploy.New(staticdeploy.Config{
		DataDir:  dataDir,
		MaxFiles: 5000,
		MaxBytes: 200 * 1024 * 1024,
	})
	projects := project.NewService(store, deployer)

	return New(Config{
		Addr:                ":0",
		WebDir:              filepath.Join(dataDir, "web"),
		SitesDir:            filepath.Join(dataDir, "sites"),
		MaxUploadSize:       100 * 1024 * 1024,
		DisableAuthForTests: true,
	}, projects, nil)
}

func staticZip(t *testing.T) []byte {
	t.Helper()
	return zipWithFile(t, "index.html", "<!doctype html><h1>StackNest Test</h1>")
}

func zipWithFile(t *testing.T, name, content string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create(name)
	if err != nil {
		t.Fatalf("create zip file: %v", err)
	}
	if _, err := file.Write([]byte(content)); err != nil {
		t.Fatalf("write zip file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buffer.Bytes()
}

func createProjectRequest(t *testing.T, name, slug string) *http.Request {
	t.Helper()
	return createProjectRequestWithArchive(t, name, slug, staticZip(t))
}

func createProjectRequestWithArchive(t *testing.T, name, slug string, archive []byte) *http.Request {
	t.Helper()
	return createProjectRequestWithNamedArchive(t, name, slug, "site.zip", archive)
}

// createProjectRequestWithNamedArchive 构造指定文件名的上传请求。
func createProjectRequestWithNamedArchive(t *testing.T, name, slug, fileName string, archive []byte) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("name", name); err != nil {
		t.Fatalf("write name: %v", err)
	}
	if slug != "" {
		if err := writer.WriteField("slug", slug); err != nil {
			t.Fatalf("write slug: %v", err)
		}
	}

	file, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	if _, err := file.Write(archive); err != nil {
		t.Fatalf("write multipart file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/projects", &body)
	request.Host = "localhost:8080"
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

// TestRARProjectLifecycle 验证真实 RAR4 和 RAR5 都能发布并被站点路由读取。
func TestRARProjectLifecycle(t *testing.T) {
	for _, version := range []string{"v4", "v5"} {
		t.Run(version, func(t *testing.T) {
			archive, err := os.ReadFile(filepath.Join("..", "staticdeploy", "testdata", "site-"+version+".rar"))
			if err != nil {
				t.Fatal(err)
			}
			server := newTestServer(t)
			create := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(create, createProjectRequestWithNamedArchive(t, "RAR Site", "rar-site", "site.rar", archive))
			if create.Code != http.StatusCreated {
				t.Fatalf("expected create 201, got %d: %s", create.Code, create.Body.String())
			}
			site := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(site, previewRequest("rar-site"))
			if site.Code != http.StatusOK || !strings.Contains(site.Body.String(), "RAR"+strings.TrimPrefix(version, "v")+" Test") {
				t.Fatalf("RAR site inaccessible: %d %s", site.Code, site.Body.String())
			}
		})
	}
}

// TestRARFileNameMustMatchArchive 拒绝把 ZIP 内容伪装成 RAR 上传。
func TestRARFileNameMustMatchArchive(t *testing.T) {
	recorder := httptest.NewRecorder()
	newTestServer(t).http.Handler.ServeHTTP(recorder, createProjectRequestWithNamedArchive(t, "Wrong Format", "wrong-format", "site.rar", staticZip(t)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestOpenAPIEndpoint(t *testing.T) {
	recorder := httptest.NewRecorder()
	newTestServer(t).http.Handler.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil),
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "openapi: 3.1.0") {
		t.Fatal("OpenAPI 3.1 document not returned")
	}
}

func TestStaticProjectLifecycle(t *testing.T) {
	server := newTestServer(t)

	createRecorder := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(
		createRecorder,
		createProjectRequest(t, "Landing Page", "landing-page"),
	)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("expected create 201, got %d: %s", createRecorder.Code, createRecorder.Body.String())
	}

	var created generated.Project
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if created.Slug != "landing-page" || created.Status != generated.ProjectStatusOnline {
		t.Fatalf("unexpected project: %+v", created)
	}
	if created.SiteUrl != "http://stack-nest-landing-page.localhost:8080/" {
		t.Fatalf("unexpected site URL: %s", created.SiteUrl)
	}

	siteRecorder := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(
		siteRecorder,
		previewRequest("landing-page"),
	)
	if siteRecorder.Code != http.StatusOK {
		t.Fatalf("expected site 200, got %d", siteRecorder.Code)
	}
	if !strings.Contains(siteRecorder.Body.String(), "StackNest Test") {
		t.Fatal("published site content missing")
	}

	deleteRecorder := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(
		deleteRecorder,
		httptest.NewRequest(http.MethodDelete, "/api/projects/landing-page", nil),
	)
	if deleteRecorder.Code != http.StatusNoContent {
		t.Fatalf("expected delete 204, got %d", deleteRecorder.Code)
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, ok := safeJoin(root, "../secret.txt"); ok {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestProjectZipWithoutIndexReturnsBadRequest(t *testing.T) {
	recorder := httptest.NewRecorder()
	newTestServer(t).http.Handler.ServeHTTP(
		recorder,
		createProjectRequestWithArchive(t, "No Index", "no-index", zipWithFile(t, "readme.txt", "missing index")),
	)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "index.html") {
		t.Fatalf("unexpected error: %s", recorder.Body.String())
	}
}

func TestRequestSchemeRejectsUntrustedValue(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Forwarded-Proto", "javascript")

	if got := requestScheme(request); got != "http" {
		t.Fatalf("expected http fallback, got %q", got)
	}
}

// TestProjectSubdomainURL 验证公网项目链接包含固定前缀并沿用 HTTPS 协议。
func TestProjectSubdomainURL(t *testing.T) {
	server := newTestServer(t)
	server.config.BaseDomain = "zby909.top"
	request := createProjectRequest(t, "Test HTML", "testhtml")
	request.Host = "stack-nest.zby909.top"
	request.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var created generated.Project
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.SiteUrl != "https://stack-nest-testhtml.zby909.top" {
		t.Fatalf("unexpected site URL: %s", created.SiteUrl)
	}
}

// TestSlugLengthReservesSitePrefix 验证项目 slug 加上域名前缀后不超过单个 DNS 标签。
func TestSlugLengthReservesSitePrefix(t *testing.T) {
	server := newTestServer(t)
	tooLong := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(tooLong, createProjectRequest(t, "Too Long", strings.Repeat("a", 53)))
	if tooLong.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for 53 characters, got %d", tooLong.Code)
	}
	valid := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(valid, createProjectRequest(t, "Valid", strings.Repeat("a", 52)))
	if valid.Code != http.StatusCreated {
		t.Fatalf("expected 201 for 52 characters, got %d: %s", valid.Code, valid.Body.String())
	}
}

func TestExplicitSlugMustFollowDNSLabelRules(t *testing.T) {
	recorder := httptest.NewRecorder()
	newTestServer(t).http.Handler.ServeHTTP(
		recorder,
		createProjectRequest(t, "Landing Page", "-bad-slug-"),
	)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", recorder.Code)
	}
}

// TestChineseNameGetsAutomaticSlug 验证中文名称可以省略 slug 且重复名称互不冲突。
func TestChineseNameGetsAutomaticSlug(t *testing.T) {
	server := newTestServer(t)
	previousSlug := ""
	for range 2 {
		recorder := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(recorder, createProjectRequest(t, "中文项目", ""))
		if recorder.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
		}
		var created generated.Project
		if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(created.Slug, "project-") || len(created.Slug) > project.MaxSlugLength || created.Slug == previousSlug {
			t.Fatalf("unexpected generated slug: %q", created.Slug)
		}
		previousSlug = created.Slug
		site := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(site, previewRequest(created.Slug))
		if site.Code != http.StatusOK || !strings.Contains(site.Body.String(), "StackNest Test") {
			t.Fatalf("generated site inaccessible: %d %s", site.Code, site.Body.String())
		}
	}
}

// previewRequest 模拟隔离项目域名上的站点请求。
func previewRequest(slug string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "http://stack-nest-"+slug+".localhost:8080/sites/"+slug+"/", nil)
}

// SaveDraft 模拟待发布版本事务。
func (s *memoryStore) SaveDraft(ctx context.Context, slug, ownerID, expectedID string, next project.Deployment) error {
	value, err := s.Get(ctx, slug)
	if err != nil {
		return err
	}
	if value.OwnerID != ownerID {
		return project.ErrNotFound
	}
	if project.CurrentID(value) != expectedID {
		return project.ErrVersionConflict
	}
	if value.Draft != nil {
		return project.ErrDraftExists
	}
	value.Draft, value.UpdatedAt = &next, next.CreatedAt
	s.projects[slug] = value
	return nil
}

// DeleteDraft 模拟按草稿编号删除，正式版不会被误删。
func (s *memoryStore) DeleteDraft(ctx context.Context, slug, ownerID, draftID string) error {
	value, err := s.Get(ctx, slug)
	if err != nil {
		return err
	}
	if value.OwnerID != ownerID {
		return project.ErrNotFound
	}
	if value.Draft == nil || value.Draft.ID != draftID {
		return project.ErrVersionConflict
	}
	value.Draft = nil
	s.projects[slug] = value
	return nil
}
