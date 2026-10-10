package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/postgres"
	"stacknest/server/internal/storage"
)

type appTestRuntime struct{}

// Health 提供仅测试使用的可用状态。
func (appTestRuntime) Health(context.Context) error { return nil }

// Operate 不访问 Docker daemon。
func (appTestRuntime) Operate(context.Context, containerapp.Operation) (containerapp.Result, error) {
	return containerapp.Result{State: "running", ContainerID: "aaaaaaaaaaaa", ImageID: "test-image"}, nil
}

// State 不泄漏底层容器配置。
func (appTestRuntime) State(_ context.Context, _ string, cid string) (containerapp.Result, error) {
	return containerapp.Result{State: "running", ContainerID: cid, Health: "healthy", HealthCheck: "http", Usage: &containerapp.Usage{MemoryBytes: 1234, MemoryLimitBytes: 128 << 20, PIDs: 3}}, nil
}

// Logs 返回可用于遮盖检查的测试输出。
func (appTestRuntime) Logs(context.Context, string, string) (string, error) {
	return "secret-app-value", nil
}

// Proxy 识别应用自己的 API 路径。
func (appTestRuntime) Proxy(w http.ResponseWriter, r *http.Request, _, _ string) {
	_, _ = w.Write([]byte("application:" + r.URL.Path))
}

// TestContainerAppsIntegration 验证真实数据库、会话、CSRF、所有权、配额与转交。
func TestContainerAppsIntegration(t *testing.T) {
	databaseURL := os.Getenv("STACKNEST_TEST_AUTH_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("temporary auth database required")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Path != "/stacknest_auth_test" {
		t.Fatal("dedicated test database required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("TRUNCATE auth_users CASCADE"); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret := "test-only-secret-with-at-least-thirty-two-bytes"
	mailer := &testMailer{codes: map[string]string{}, sends: map[string]int{}}
	authentication, err := auth.NewService(store, mailer, secret)
	if err != nil {
		t.Fatal(err)
	}
	if err = authentication.BootstrapUsers(ctx, "admin@example.com", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	foreign, err := authentication.AddUser(ctx, "foreign@example.com")
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := store.FindUserByEmail(ctx, "owner@example.com")
	admin, _ := store.FindUserByEmail(ctx, "admin@example.com")
	root := t.TempDir()
	apps := containerapp.New(store, appTestRuntime{}, secret, storage.New(root), nil)
	server := New(Config{Apps: apps, Addr: ":0", SitesDir: root, WebDir: root}, nil, authentication)
	// cookie 通过真实一次性验证码创建会话，不绕过认证。
	cookie := func(email string) *http.Cookie {
		if err := authentication.RequestCode(ctx, email); err != nil {
			t.Fatal(err)
		}
		_, token, err := authentication.VerifyCode(ctx, email, mailer.codes[email])
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: sessionCookie, Value: token}
	}
	ownerCookie, foreignCookie, adminCookie := cookie(owner.Email), cookie(foreign.Email), cookie(admin.Email)
	// call 携带真实会话和同源请求头调用完整 HTTP 中间件。
	call := func(method, path, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = "localhost:8080"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(out, req)
		return out
	}
	input := `{"name":"First app","slug":"first","image":"example/app:v1","port":3000,"memoryMiB":128,"cpus":0.5,"dataPath":"/app/data","healthPath":"/health","env":{"SECRET":"secret-app-value"}}`
	for _, attempt := range []struct {
		cookie *http.Cookie
		origin string
		want   int
	}{{nil, "http://localhost:8080", 401}, {ownerCookie, "https://attacker.example", 403}} {
		if got := call("POST", "/api/apps", input, attempt.cookie, attempt.origin).Code; got != attempt.want {
			t.Fatalf("auth or csrf: %d", got)
		}
	}
	invalid := strings.TrimSuffix(input, "}") + `,"hostNetwork":true}`
	if got := call("POST", "/api/apps", invalid, ownerCookie, "http://localhost:8080").Code; got != 400 {
		t.Fatalf("unsafe parameter: %d", got)
	}
	created := call("POST", "/api/apps", input, ownerCookie, "http://localhost:8080")
	if created.Code != 202 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var response struct{ App containerapp.App }
	if json.Unmarshal(created.Body.Bytes(), &response) != nil || response.App.ID == "" {
		t.Fatal(created.Body.String())
	}
	id := response.App.ID
	if response.App.HealthPath != "/health" {
		t.Fatal("health path was not preserved")
	}
	usage := call("GET", "/api/apps/"+id, "", ownerCookie, "")
	if usage.Code != 200 || !strings.Contains(usage.Body.String(), `"memoryBytes":1234`) || !strings.Contains(usage.Body.String(), `"health":"healthy"`) {
		t.Fatal("authorized application usage missing", usage.Body.String())
	}
	var sealed string
	if err = db.QueryRow("SELECT sealed FROM container_apps WHERE id=$1", id).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "secret-app-value") || strings.Contains(created.Body.String(), "secret-app-value") {
		t.Fatal("secret leaked")
	}
	for _, path := range []string{"/api/apps/" + id, "/api/apps/" + id + "/logs"} {
		if got := call("GET", path, "", foreignCookie, "").Code; got != 404 {
			t.Fatalf("foreign access: %d", got)
		}
		if got := call("GET", path, "", adminCookie, "").Code; got != 200 {
			t.Fatalf("admin access: %d", got)
		}
	}
	listing := call("GET", "/api/apps", "", foreignCookie, "")
	if listing.Code != 200 || strings.Contains(listing.Body.String(), id) {
		t.Fatal("foreign list leaked application")
	}
	claimed, err := store.ClaimApp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimed.ActiveSealed = claimed.Sealed
	if err = store.CompleteApp(ctx, claimed, containerapp.Result{ContainerID: "aaaaaaaaaaaa", ImageID: "test", State: "running"}, ""); err != nil {
		t.Fatal(err)
	}
	log := call("GET", "/api/apps/"+id+"/logs", "", ownerCookie, "")
	if log.Code != 200 || strings.Contains(log.Body.String(), "secret-app-value") {
		t.Fatal("unredacted logs")
	}
	// 公共应用来源的 /api/auth/session 属于应用，而不是后台会话接口。
	req := httptest.NewRequest("GET", "http://stack-nest--app-first.localhost:8080/api/auth/session", nil)
	out := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(out, req)
	if out.Body.String() != "application:/api/auth/session" {
		t.Fatal("app route crossed admin boundary", out.Body.String())
	}
	codes := make(chan int, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			codes <- call("POST", "/api/apps/"+id+"/actions", `{"action":"stop","generation":1}`, ownerCookie, "http://localhost:8080").Code
		}()
	}
	wait.Wait()
	close(codes)
	accepted, conflicts := 0, 0
	for code := range codes {
		if code == 202 {
			accepted++
		}
		if code == 409 {
			conflicts++
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatal("concurrent lifecycle requests both accepted")
	}
	claimed, err = store.ClaimApp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RecoverApps(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.ClaimApp(ctx)
	if err != nil || recovered.Generation != claimed.Generation {
		t.Fatal("interrupted job not recovered", err)
	}
	if err = store.CompleteApp(ctx, recovered, containerapp.Result{ContainerID: "aaaaaaaaaaaa", State: "exited"}, ""); err != nil {
		t.Fatal(err)
	}
	// 并发创建由数据库事务保证总额，不能因为页面同时提交而超配。
	quota := make(chan error, 2)
	actor := containerapp.Actor{ID: owner.ID, Email: owner.Email}
	for i := range 2 {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, err := apps.Save(ctx, "", actor, containerapp.Input{Name: "Quota", Slug: fmt.Sprintf("quota-%d", i), Spec: containerapp.Spec{Image: "example/app:v1", Port: 3000, MemoryMiB: 512, CPUs: 0.5}}, 0)
			quota <- err
		}(i)
	}
	wait.Wait()
	close(quota)
	success := 0
	for err := range quota {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("quota over-allocation: %d", success)
	}
	if err = authentication.DeleteUser(ctx, owner.ID); err != nil {
		t.Fatal(err)
	}
	transferred, err := store.App(ctx, id, containerapp.Actor{ID: admin.ID, Admin: true})
	if err != nil || transferred.OwnerID != admin.ID {
		t.Fatal("application was lost when removing email", err)
	}
	if got := call("GET", "/api/apps", "", ownerCookie, "").Code; got != 401 {
		t.Fatal("deleted user's session remained valid")
	}
}
