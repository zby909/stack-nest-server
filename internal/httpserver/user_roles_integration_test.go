package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/postgres"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRoleChangesIntegration 使用真实数据库验证角色提升、撤销凭证、越权与并发冲突。
func TestRoleChangesIntegration(t *testing.T) {
	raw := os.Getenv("STACKNEST_TEST_AUTH_DATABASE_URL")
	if raw == "" {
		t.Skip("dedicated auth database required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path != "/stacknest_auth_test" {
		t.Fatal("dedicated auth database required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("TRUNCATE auth_users CASCADE"); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mailer := &testMailer{codes: map[string]string{}, sends: map[string]int{}}
	authentication, err := auth.NewService(store, mailer, "test-only-secret-with-at-least-thirty-two-bytes")
	if err != nil {
		t.Fatal(err)
	}
	if err = authentication.BootstrapUsers(ctx, "super@example.com", "member@example.com"); err != nil {
		t.Fatal(err)
	}
	if err = authentication.SetSuperAdmin(ctx, "super@example.com"); err != nil {
		t.Fatal(err)
	}
	audit, err := activity.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := New(Config{SitesDir: t.TempDir(), WebDir: t.TempDir(), Activity: audit}, nil, authentication)
	// login 走真实验证码交换，记录的 Cookie 不写入测试输出。
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
	// call 覆盖身份、同源保护及路由，而非直接调用角色存储方法。
	call := func(method, path, body string, cookie *http.Cookie, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://localhost:18080"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(out, req)
		return out
	}
	origin := "http://localhost:18080"
	superCookie, oldCookie := login("super@example.com"), login("member@example.com")
	super, _ := store.FindUserByEmail(ctx, "super@example.com")
	member, _ := store.FindUserByEmail(ctx, "member@example.com")
	path := "/api/users/" + member.ID + "/role"
	promote := `{"role":"admin","expectedRole":"user"}`
	demote := `{"role":"user","expectedRole":"admin"}`
	for _, attempt := range []struct {
		cookie             *http.Cookie
		path, body, origin string
		status             int
	}{
		{nil, path, promote, origin, 401}, {oldCookie, path, promote, origin, 403},
		{superCookie, path, promote, "https://attacker.example", 403},
		{superCookie, path, `{"role":"superadmin","expectedRole":"user"}`, origin, 400},
		{superCookie, path, `{"role":"admin","expectedRole":"admin"}`, origin, 400},
		{superCookie, "/api/users/" + super.ID + "/role", demote, origin, 403},
	} {
		if out := call("PUT", attempt.path, attempt.body, attempt.cookie, attempt.origin); out.Code != attempt.status {
			t.Fatalf("role boundary: %d %s", out.Code, out.Body.String())
		}
	}
	now := time.Now()
	if err = store.SaveChallenge(ctx, member.ID, "old-code-hash", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if out := call("PUT", path, promote, superCookie, origin); out.Code != 200 {
		t.Fatal("promote", out.Body.String())
	}
	if out := call("GET", "/api/auth/session", "", oldCookie, ""); out.Code != 401 {
		t.Fatal("old session survived promotion")
	}
	if err = store.ExchangeChallenge(ctx, member.ID, "old-code-hash", "forbidden-token", now, now.Add(time.Hour)); !errors.Is(err, auth.ErrInvalidCode) {
		t.Fatal("old challenge survived", err)
	}
	if out := call("PUT", path, promote, superCookie, origin); out.Code != 409 {
		t.Fatal("stale promotion accepted")
	}
	adminCookie := login("member@example.com")
	if out := call("GET", "/api/users", "", adminCookie, ""); out.Code != 200 {
		t.Fatal("new admin cannot list users")
	}
	if out := call("PUT", path, demote, adminCookie, origin); out.Code != 403 {
		t.Fatal("ordinary admin changed roles")
	}
	if out := call("PUT", path, demote, superCookie, origin); out.Code != 200 {
		t.Fatal("demotion failed")
	}
	if out := call("GET", "/api/auth/session", "", adminCookie, ""); out.Code != 401 {
		t.Fatal("admin session survived demotion")
	}
	userCookie := login("member@example.com")
	if out := call("GET", "/api/users", "", userCookie, ""); out.Code != 403 {
		t.Fatal("demoted account retained admin access")
	}
	// 同一旧角色提交两次时，数据库锁保证仅一条操作成功。
	codes := make(chan int, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() { defer wait.Done(); codes <- call("PUT", path, promote, superCookie, origin).Code }()
	}
	wait.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent role update", counts)
	}
	events, err := audit.Query(ctx, activity.Query{IsAdmin: true, Page: 1, PageSize: 20, Action: "user-role"})
	if err != nil || events.Total != 3 {
		t.Fatal("missing role audit", events.Total, err)
	}
	if mailer.inviteCalls != 0 {
		t.Fatal("role update sent invitations")
	}
}
