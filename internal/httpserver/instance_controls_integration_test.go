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
	"stacknest/server/internal/auth"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/postgres"
	"stacknest/server/internal/storage"
	"strings"
	"sync"
	"testing"
)

// TestInstanceControlsIntegration 验证真实会话、角色授予、配额持久化和并发边界。
func TestInstanceControlsIntegration(t *testing.T) {
	databaseURL := os.Getenv("STACKNEST_TEST_AUTH_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("dedicated auth database required")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Path != "/stacknest_auth_test" {
		t.Fatal("dedicated auth database required")
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
	if _, err = db.Exec("UPDATE app_limits SET max_apps=3,total_memory_mib=768,revision=1"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("UPDATE app_limits SET max_apps=3,total_memory_mib=768,revision=1")
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mailer := &testMailer{codes: map[string]string{}, sends: map[string]int{}}
	authentication, err := auth.NewService(store, mailer, "test-only-secret-with-at-least-thirty-two-bytes")
	if err != nil {
		t.Fatal(err)
	}
	if err = authentication.BootstrapUsers(ctx, "super@example.com", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err = authentication.SetSuperAdmin(ctx, "super@example.com"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	apps := containerapp.New(store, appTestRuntime{}, "test-only-secret-with-at-least-thirty-two-bytes", storage.New(root), nil)
	server := New(Config{Apps: apps, SitesDir: root, WebDir: root, InvitationLoginURL: "https://stack.example.com/login"}, nil, authentication)
	// login 使用真实验证码交换测试邮件中的代码，不绕过认证服务。
	login := func(email string) *http.Cookie {
		t.Helper()
		if err := authentication.RequestCode(ctx, email); err != nil {
			t.Fatal(err)
		}
		_, token, err := authentication.VerifyCode(ctx, email, mailer.codes[email])
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: sessionCookie, Value: token}
	}
	// call 经完整 HTTP 中间件验证身份、CSRF 和管理路由。
	call := func(method, path, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://localhost:18080"+path, strings.NewReader(body))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(out, req)
		return out
	}
	origin := "http://localhost:18080"
	superCookie, ownerCookie := login("super@example.com"), login("owner@example.com")
	adminResult := call("POST", "/api/users", `{"email":"other-admin@example.com","role":"admin"}`, superCookie, origin)
	if adminResult.Code != 201 || !strings.Contains(adminResult.Body.String(), `"role":"admin"`) || mailer.inviteCalls != 0 {
		t.Fatal("admin not created safely", adminResult.Body.String())
	}
	adminCookie := login("other-admin@example.com")
	for _, attempt := range []struct {
		cookie       *http.Cookie
		body, origin string
		want         int
	}{
		{nil, `{"email":"blocked@example.com","role":"admin"}`, origin, 401},
		{ownerCookie, `{"email":"blocked@example.com","role":"admin"}`, origin, 403},
		{adminCookie, `{"email":"blocked@example.com","role":"admin"}`, origin, 403},
		{superCookie, `{"email":"blocked@example.com","role":"superadmin"}`, origin, 400},
		{superCookie, `{"email":"blocked@example.com","role":"other"}`, origin, 400},
		{superCookie, `{"email":"blocked@example.com","role":"admin"}`, "https://attacker.example", 403},
		{superCookie, `{"email":"owner@example.com","role":"admin"}`, origin, 409},
	} {
		if out := call("POST", "/api/users", attempt.body, attempt.cookie, attempt.origin); out.Code != attempt.want {
			t.Fatalf("role boundary %d %s", out.Code, out.Body.String())
		}
	}
	owner, _ := store.FindUserByEmail(ctx, "owner@example.com")
	if owner.Role != auth.RoleUser {
		t.Fatal("existing user was promoted by duplicate add")
	}
	added, _ := store.FindUserByEmail(ctx, "other-admin@example.com")
	if out := call("DELETE", "/api/users/"+added.ID, "", superCookie, origin); out.Code != 403 {
		t.Fatal("existing admin deletion rule changed")
	}
	for _, cookie := range []*http.Cookie{ownerCookie, adminCookie} {
		for _, method := range []string{"GET", "PUT"} {
			if out := call(method, "/api/apps/limits", `{"maxApps":5,"totalMemoryMiB":1024,"expectedRevision":1}`, cookie, origin); out.Code != 403 {
				t.Fatal("non-super accessed resources", out.Body.String())
			}
		}
	}
	if out := call("PUT", "/api/apps/limits", `{"maxApps":5,"totalMemoryMiB":1024,"expectedRevision":1}`, superCookie, "https://attacker.example"); out.Code != 403 {
		t.Fatal("resource csrf bypass")
	}
	if out := call("PUT", "/api/apps/limits", `{"maxApps":5,"totalMemoryMiB":8192,"expectedRevision":1}`, superCookie, origin); out.Code != 400 {
		t.Fatal("host capacity bypass")
	}
	changed := call("PUT", "/api/apps/limits", `{"maxApps":5,"totalMemoryMiB":1024,"expectedRevision":1}`, superCookie, origin)
	if changed.Code != 200 {
		t.Fatal("save limits", changed.Body.String())
	}
	var settings containerapp.LimitsView
	if json.Unmarshal(changed.Body.Bytes(), &settings) != nil || settings.Revision != 2 || settings.MaxTotalMemoryMiB != 4096 {
		t.Fatal("incorrect limits response")
	}
	if out := call("PUT", "/api/apps/limits", `{"maxApps":5,"totalMemoryMiB":1024,"expectedRevision":1}`, superCookie, origin); out.Code != 409 {
		t.Fatal("stale config overwritten")
	}
	// 第四个应用能成功，证明后台使用持久化预算而不是旧常量。
	actor := containerapp.Actor{ID: owner.ID, Email: owner.Email}
	var first containerapp.App
	for i := 0; i < 4; i++ {
		value, err := apps.Save(ctx, "", actor, containerapp.Input{Name: "Quota", Slug: fmt.Sprintf("limits-%d", i), Spec: containerapp.Spec{Image: "example/http:v1", Port: 3000, MemoryMiB: 128, CPUs: 0.5}}, 0)
		if err != nil {
			t.Fatal("configurable app count ignored", err)
		}
		if i == 0 {
			first = value
		}
	}
	if out := call("PUT", "/api/apps/limits", `{"maxApps":5,"totalMemoryMiB":1024,"expectedRevision":2}`, superCookie, origin); out.Code != 409 {
		t.Fatal("changed config while tasks pending")
	}
	for i := 0; i < 4; i++ {
		a, err := store.ClaimApp(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a.ActiveSealed = a.Sealed
		if err = store.CompleteApp(ctx, a, containerapp.Result{State: "running"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if out := call("PUT", "/api/apps/limits", `{"maxApps":3,"totalMemoryMiB":1024,"expectedRevision":2}`, superCookie, origin); out.Code != 409 {
		t.Fatal("reduced app count below use")
	}
	if out := call("PUT", "/api/apps/limits", `{"maxApps":5,"totalMemoryMiB":256,"expectedRevision":2}`, superCookie, origin); out.Code != 409 {
		t.Fatal("reduced memory below allocation")
	}
	for _, cookie := range []*http.Cookie{ownerCookie, adminCookie} {
		out := call("GET", "/api/apps", "", cookie, "")
		if out.Code != 200 || !strings.Contains(out.Body.String(), `"totalMemoryMiB":1024`) || strings.Contains(out.Body.String(), "maxTotalMemoryMiB") || strings.Contains(out.Body.String(), "allocatedMemoryMiB") {
			t.Fatal("summary leaked host information or ignored config")
		}
	}
	// 并发的两个配置保存只有一个能使用同一版本成功。
	codes := make(chan int, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			codes <- call("PUT", "/api/apps/limits", `{"maxApps":5,"totalMemoryMiB":1280,"expectedRevision":2}`, superCookie, origin).Code
		}()
	}
	wait.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent config overwritten", counts)
	}
	// 降低候选内存时旧版仍预留 128 MiB，恢复原版不会挤占其他应用额度。
	if _, err := apps.Save(ctx, first.ID, actor, containerapp.Input{Name: "Quota", Spec: containerapp.Spec{Image: "example/http:v2", Port: 3000, MemoryMiB: 64, CPUs: 0.5}}, first.Generation); err != nil {
		t.Fatal(err)
	}
	budget, err := store.AppLimits(ctx)
	if err != nil || budget.AllocatedMemoryMiB != 512 {
		t.Fatal("old version reservation released prematurely", budget, err)
	}
	// 只剩一个名额时，并发创建必须保持全局上限。
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, err := apps.Save(ctx, "", actor, containerapp.Input{Name: "Last", Slug: fmt.Sprintf("last-%d", i), Spec: containerapp.Spec{Image: "example/http:v1", Port: 3000, MemoryMiB: 64, CPUs: 0.5}}, 0)
			outcomes <- err
		}(i)
	}
	wait.Wait()
	close(outcomes)
	succeeded := 0
	for err := range outcomes {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatal("concurrent last slot overcommitted", succeeded)
	}
	reopened, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := reopened.AppLimits(ctx)
	if err != nil || saved.MaxApps != 5 || saved.TotalMemoryMiB != 1280 {
		t.Fatal("config not durable", saved, err)
	}
}
