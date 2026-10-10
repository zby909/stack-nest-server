package httpserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/postgres"
	"stacknest/server/internal/project"
	"stacknest/server/internal/staticdeploy"
	"stacknest/server/internal/systeminfo"
)

type testMailer struct {
	codes       map[string]string
	sends       map[string]int
	invites     map[string]string
	inviteCalls int
	inviteErr   error
}

// SendCode 记录测试验证码，不发送真实邮件。
func (m *testMailer) SendCode(_ context.Context, email, code string) error {
	m.codes[email] = code
	m.sends[email]++
	return nil
}

// SendInvitation 记录测试邀请和模拟发送结果，不发送真实邮件。
func (m *testMailer) SendInvitation(_ context.Context, email, loginURL string) error {
	m.inviteCalls++
	if m.invites == nil {
		m.invites = make(map[string]string)
	}
	m.invites[email] = loginURL
	return m.inviteErr
}

// TestEmailAuthIntegration 验证数据库、HTTP 权限、验证码消费及会话撤销。
func TestEmailAuthIntegration(t *testing.T) {
	databaseURL := os.Getenv("STACKNEST_TEST_AUTH_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("STACKNEST_TEST_AUTH_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Path != "/stacknest_auth_test" {
		t.Fatal("auth integration test requires a dedicated stacknest_auth_test database")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("TRUNCATE auth_users CASCADE"); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mailer := &testMailer{codes: make(map[string]string), sends: make(map[string]int)}
	service, err := auth.NewService(store, mailer, "test-only-secret-with-at-least-thirty-two-bytes")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.BootstrapUsers(context.Background(), "admin@example.com", "user@example.com"); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	activityLog, err := activity.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	projects := project.NewService(store, staticdeploy.New(staticdeploy.Config{DataDir: dataDir, MaxFiles: 5000, MaxBytes: 200 * 1024 * 1024}))
	server := New(Config{Addr: ":0", WebDir: filepath.Join(dataDir, "web"), SitesDir: filepath.Join(dataDir, "sites"), MaxUploadSize: 100 * 1024 * 1024, Activity: activityLog, CheckDatabase: store.Ping, ReadSystem: func(context.Context) (systeminfo.Usage, error) {
		return systeminfo.Usage{CPUPercent: 12.5, CPUCores: 4, MemoryTotalBytes: 1024, MemoryAvailableBytes: 512, MemoryUsedPercent: 50, UptimeSeconds: 120}, nil
	}}, projects, service)
	healthRequest(t, server, http.StatusOK, "ok")

	// 调用带同源 Origin 的管理 API，并可选择附加会话 Cookie。
	call := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Host = "localhost:8080"
		req.Header.Set("Origin", "http://localhost:8080")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		result := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(result, req)
		return result
	}
	for _, route := range []string{"/api/storage", "/api/activity", "/api/system", "/api/backups/status", "/api/uploads/00000000-0000-4000-8000-000000000000"} {
		if got := call(http.MethodGet, route, "", nil).Code; got != http.StatusUnauthorized {
			t.Fatalf("unprotected operations route %s: %d", route, got)
		}
	}
	if got := call(http.MethodGet, "/api/projects", "", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated projects: %d", got)
	}
	if got := call(http.MethodPost, "/api/auth/request-code", `{"email":"absent@example.com"}`, nil).Code; got != http.StatusAccepted {
		t.Fatalf("unknown email must have generic response: %d", got)
	}
	if got := call(http.MethodPost, "/api/auth/request-code", `{"email":"admin@example.com"}`, nil).Code; got != http.StatusAccepted {
		t.Fatalf("request admin code: %d", got)
	}
	if got := call(http.MethodPost, "/api/auth/request-code", `{"email":"admin@example.com"}`, nil).Code; got != http.StatusAccepted || mailer.sends["admin@example.com"] != 1 {
		t.Fatalf("resend cooldown failed: status %d, sends %d", got, mailer.sends["admin@example.com"])
	}
	if got := call(http.MethodPost, "/api/auth/verify", `{"email":"admin@example.com","code":"00000000"}`, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("wrong code: %d", got)
	}
	verify := call(http.MethodPost, "/api/auth/verify", `{"email":"admin@example.com","code":"`+mailer.codes["admin@example.com"]+`"}`, nil)
	if verify.Code != http.StatusOK {
		t.Fatalf("admin login: %d %s", verify.Code, verify.Body.String())
	}
	adminCookie := verify.Result().Cookies()[0]
	if !adminCookie.HttpOnly || adminCookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie security flags missing")
	}
	if got := call(http.MethodGet, "/api/auth/session", "", adminCookie).Code; got != http.StatusOK {
		t.Fatalf("current session: %d", got)
	}
	crossSite := httptest.NewRequest(http.MethodPost, "/api/users", bytes.NewBufferString(`{"email":"blocked@example.com"}`))
	crossSite.Host = "localhost:8080"
	crossSite.Header.Set("Origin", "https://evil.example")
	crossSite.Header.Set("Content-Type", "application/json")
	crossSite.AddCookie(adminCookie)
	crossSiteResult := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(crossSiteResult, crossSite)
	if crossSiteResult.Code != http.StatusForbidden {
		t.Fatalf("cross-site mutation: %d", crossSiteResult.Code)
	}
	if got := call(http.MethodPost, "/api/auth/verify", `{"email":"admin@example.com","code":"`+mailer.codes["admin@example.com"]+`"}`, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("replayed code: %d", got)
	}
	if got := call(http.MethodPost, "/api/auth/request-code", `{"email":"user@example.com"}`, nil).Code; got != http.StatusAccepted {
		t.Fatalf("request ordinary user code: %d", got)
	}
	correct := mailer.codes["user@example.com"]
	wrong := "0" + correct[1:]
	if wrong == correct {
		wrong = "1" + correct[1:]
	}
	for range 5 {
		if got := call(http.MethodPost, "/api/auth/verify", `{"email":"user@example.com","code":"`+wrong+`"}`, nil).Code; got != http.StatusBadRequest {
			t.Fatalf("wrong code attempt: %d", got)
		}
	}
	if got := call(http.MethodPost, "/api/auth/verify", `{"email":"user@example.com","code":"`+correct+`"}`, nil).Code; got != http.StatusBadRequest {
		t.Fatalf("code accepted after five failures: %d", got)
	}

	newUser := call(http.MethodPost, "/api/users", `{"email":"new@example.com"}`, adminCookie)
	if newUser.Code != http.StatusCreated {
		t.Fatalf("add user: %d %s", newUser.Code, newUser.Body.String())
	}
	var created auth.User
	if err := json.Unmarshal(newUser.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Role != auth.RoleUser {
		t.Fatalf("unexpected role: %q", created.Role)
	}

	if got := call(http.MethodPost, "/api/auth/request-code", `{"email":"new@example.com"}`, nil).Code; got != http.StatusAccepted {
		t.Fatalf("request new user code: %d", got)
	}
	userLogin := call(http.MethodPost, "/api/auth/verify", `{"email":"new@example.com","code":"`+mailer.codes["new@example.com"]+`"}`, nil)
	if userLogin.Code != http.StatusOK {
		t.Fatalf("user login: %d", userLogin.Code)
	}
	userCookie := userLogin.Result().Cookies()[0]
	projectsResponse := call(http.MethodGet, "/api/projects", "", userCookie)
	if projectsResponse.Code != http.StatusOK {
		t.Fatalf("ordinary user projects: %d", projectsResponse.Code)
	}
	if projectsResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("authenticated API response must not be cached")
	}
	createRequest := createProjectRequest(t, "Ordinary Site", "ordinary-site")
	createRequest.Header.Set("Origin", "http://localhost:8080")
	createRequest.AddCookie(userCookie)
	createdProject := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(createdProject, createRequest)
	if createdProject.Code != http.StatusCreated {
		t.Fatalf("ordinary user create project: %d %s", createdProject.Code, createdProject.Body.String())
	}
	owner, err := store.FindUserByEmail(context.Background(), "new@example.com")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "ordinary-site")
	if err != nil || stored.OwnerID != owner.ID || stored.OwnerEmail != owner.Email {
		t.Fatalf("project owner was not persisted: %+v %v", stored, err)
	}
	adminUpload := createProjectRequest(t, "Admin Site", "admin-site")
	adminUpload.Header.Set("Origin", "http://localhost:8080")
	adminUpload.AddCookie(adminCookie)
	adminCreated := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(adminCreated, adminUpload)
	if adminCreated.Code != http.StatusCreated {
		t.Fatalf("admin create: %d %s", adminCreated.Code, adminCreated.Body.String())
	}
	if got := call(http.MethodGet, "/api/projects/admin-site", "", userCookie).Code; got != http.StatusNotFound {
		t.Fatalf("ordinary user read admin project: %d", got)
	}
	if got := call(http.MethodDelete, "/api/projects/admin-site", "", userCookie).Code; got != http.StatusNotFound {
		t.Fatalf("ordinary user deleted admin project: %d", got)
	}
	var ownedList struct {
		Projects []generated.Project `json:"projects"`
	}
	if err := json.Unmarshal(call(http.MethodGet, "/api/projects", "", userCookie).Body.Bytes(), &ownedList); err != nil || len(ownedList.Projects) != 1 || ownedList.Projects[0].Slug != "ordinary-site" {
		t.Fatalf("ordinary user list leaked projects: %+v %v", ownedList, err)
	}
	var adminList struct {
		Projects []generated.Project `json:"projects"`
	}
	if err := json.Unmarshal(call(http.MethodGet, "/api/projects", "", adminCookie).Body.Bytes(), &adminList); err != nil || len(adminList.Projects) != 2 {
		t.Fatalf("admin cannot list all projects: %+v %v", adminList, err)
	}
	for _, value := range append(ownedList.Projects, adminList.Projects...) {
		if value.Deployments == nil || (*value.Deployments)[0].Files == nil || (*value.Deployments)[0].Files.Count != 1 {
			t.Fatalf("authorized project missing file usage: %+v", value)
		}
	}
	if got := call(http.MethodPost, "/api/users", `{"email":"other@example.com"}`, adminCookie).Code; got != http.StatusCreated {
		t.Fatalf("add second user: %d", got)
	}
	if got := call(http.MethodPost, "/api/auth/request-code", `{"email":"other@example.com"}`, nil).Code; got != http.StatusAccepted {
		t.Fatalf("request second user code: %d", got)
	}
	otherLogin := call(http.MethodPost, "/api/auth/verify", `{"email":"other@example.com","code":"`+mailer.codes["other@example.com"]+`"}`, nil)
	if otherLogin.Code != http.StatusOK {
		t.Fatalf("second user login: %d %s", otherLogin.Code, otherLogin.Body.String())
	}
	otherCookie := otherLogin.Result().Cookies()[0]
	if err := json.Unmarshal(call(http.MethodGet, "/api/projects", "", otherCookie).Body.Bytes(), &ownedList); err != nil || len(ownedList.Projects) != 0 {
		t.Fatalf("second user sees another owner's project: %+v %v", ownedList, err)
	}
	if got := call(http.MethodGet, "/api/projects/ordinary-site", "", otherCookie).Code; got != http.StatusNotFound {
		t.Fatalf("second user read another user's project: %d", got)
	}
	if got := call(http.MethodDelete, "/api/projects/ordinary-site", "", otherCookie).Code; got != http.StatusNotFound {
		t.Fatalf("second user deleted another user's project: %d", got)
	}
	var ownEvents, otherEvents, allEvents struct {
		Events []activity.Event `json:"events"`
	}
	if err := json.Unmarshal(call(http.MethodGet, "/api/activity", "", userCookie).Body.Bytes(), &ownEvents); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(call(http.MethodGet, "/api/activity", "", otherCookie).Body.Bytes(), &otherEvents); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(call(http.MethodGet, "/api/activity", "", adminCookie).Body.Bytes(), &allEvents); err != nil {
		t.Fatal(err)
	}
	if len(ownEvents.Events) == 0 || len(otherEvents.Events) != 1 || len(allEvents.Events) <= len(ownEvents.Events) {
		t.Fatalf("activity isolation failed: own=%d other=%d admin=%d", len(ownEvents.Events), len(otherEvents.Events), len(allEvents.Events))
	}
	for _, event := range ownEvents.Events {
		if event.ActorID != owner.ID && event.ProjectOwnerID != owner.ID {
			t.Fatalf("ordinary user saw foreign activity: %+v", event)
		}
	}
	for _, route := range []string{"/api/storage", "/api/activity"} {
		if got := call(http.MethodGet, route, "", userCookie).Code; got != http.StatusOK {
			t.Fatalf("ordinary user operations route %s: %d", route, got)
		}
	}
	if got := call(http.MethodGet, "/api/system", "", userCookie).Code; got != http.StatusForbidden {
		t.Fatalf("ordinary user system status: %d", got)
	}
	var ordinaryStorage map[string]any
	if err := json.Unmarshal(call(http.MethodGet, "/api/storage", "", userCookie).Body.Bytes(), &ordinaryStorage); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"totalBytes", "freeBytes", "usedPercent", "maxUsedPercent", "reservedUploadBytes"} {
		if _, exists := ordinaryStorage[field]; exists {
			t.Fatalf("ordinary user saw disk field %s", field)
		}
	}
	if ordinaryStorage["uploadAllowed"] == nil || ordinaryStorage["maxUploadBytes"] == nil {
		t.Fatalf("ordinary upload policy missing: %v", ordinaryStorage)
	}
	var adminSystem map[string]any
	adminSystemResponse := call(http.MethodGet, "/api/system", "", adminCookie)
	if adminSystemResponse.Code != http.StatusOK || json.Unmarshal(adminSystemResponse.Body.Bytes(), &adminSystem) != nil || adminSystem["cpuPercent"] == nil || adminSystem["diskTotalBytes"] == nil {
		t.Fatalf("admin system status: %d %s", adminSystemResponse.Code, adminSystemResponse.Body.String())
	}
	events, err := activityLog.List()
	if err != nil || len(events) == 0 {
		t.Fatalf("missing activity: %v %v", events, err)
	}
	foundOwner := false
	for _, event := range events {
		if event.ActorID == owner.ID && event.Actor == owner.Email {
			foundOwner = true
		}
	}
	if !foundOwner {
		t.Fatal("project actor identity missing from activity")
	}
	exerciseAuthenticatedRevisions(t, server, userCookie, otherCookie, adminCookie)
	exerciseAuthenticatedManagement(t, server, userCookie, otherCookie, adminCookie)
	exerciseHTTPSCookies(t, server, service, mailer)
	previewAPI := httptest.NewRequest(http.MethodPost, "http://stack-nest-ordinary-site.localhost:8080/api/users", bytes.NewBufferString(`{"email":"not-added@example.com"}`))
	previewAPI.Header.Set("Origin", "http://stack-nest-ordinary-site.localhost:8080")
	previewAPI.Header.Set("Content-Type", "application/json")
	previewAPI.AddCookie(adminCookie)
	previewResult := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(previewResult, previewAPI)
	if previewResult.Code != http.StatusNotFound {
		t.Fatalf("preview used admin cookie: %d", previewResult.Code)
	}
	if got := call(http.MethodDelete, "/api/projects/admin-site", "", adminCookie).Code; got != http.StatusNoContent {
		t.Fatalf("admin delete own project: %d", got)
	}
	if got := call(http.MethodDelete, "/api/projects/ordinary-site", "", userCookie).Code; got != http.StatusNoContent {
		t.Fatalf("ordinary user delete project: %d", got)
	}
	transferUpload := createProjectRequest(t, "Transfer Site", "transfer-site")
	transferUpload.Header.Set("Origin", "http://localhost:8080")
	transferUpload.AddCookie(userCookie)
	transferCreated := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(transferCreated, transferUpload)
	if transferCreated.Code != http.StatusCreated {
		t.Fatalf("create transfer project: %d %s", transferCreated.Code, transferCreated.Body.String())
	}
	if got := call(http.MethodGet, "/api/users", "", userCookie).Code; got != http.StatusForbidden {
		t.Fatalf("ordinary user list users: %d", got)
	}
	if got := call(http.MethodDelete, "/api/users/"+created.ID, "", adminCookie).Code; got != http.StatusNoContent {
		t.Fatalf("delete user: %d", got)
	}
	admin, err := store.FindUserByEmail(context.Background(), "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	transferred, err := store.Get(context.Background(), "transfer-site")
	if err != nil || transferred.OwnerID != admin.ID || transferred.OwnerEmail != admin.Email {
		t.Fatalf("removed user's project not transferred: %+v %v", transferred, err)
	}
	if got := call(http.MethodDelete, "/api/projects/transfer-site", "", adminCookie).Code; got != http.StatusNoContent {
		t.Fatalf("admin cannot delete transferred project: %d", got)
	}
	if got := call(http.MethodGet, "/api/projects", "", userCookie).Code; got != http.StatusUnauthorized {
		t.Fatalf("deleted user's session must be revoked: %d", got)
	}
	if got := call(http.MethodPost, "/api/auth/logout", "", adminCookie).Code; got != http.StatusNoContent {
		t.Fatalf("logout: %d", got)
	}
	if got := call(http.MethodGet, "/api/projects", "", adminCookie).Code; got != http.StatusUnauthorized {
		t.Fatalf("logged-out session accepted: %d", got)
	}
}
