package httpserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/postgres"
)

// TestUserInvitationsIntegration 验证默认不发邀请、管理员权限、固定地址与发信失败后保留邮箱。
func TestUserInvitationsIntegration(t *testing.T) {
	databaseURL := os.Getenv("STACKNEST_TEST_AUTH_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("STACKNEST_TEST_AUTH_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Path != "/stacknest_auth_test" {
		t.Fatal("invitation integration test requires a dedicated stacknest_auth_test database")
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
	server := New(Config{SitesDir: t.TempDir(), InvitationLoginURL: "https://stack.example.com/login"}, nil, service)

	// 创建测试会话，避免为邀请生成一次性验证码或触碰真实邮件。
	login := func(email string) *http.Cookie {
		t.Helper()
		if err := service.RequestCode(context.Background(), email); err != nil {
			t.Fatal(err)
		}
		_, token, err := service.VerifyCode(context.Background(), email, mailer.codes[email])
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: sessionCookie, Value: token}
	}
	adminCookie, userCookie := login("admin@example.com"), login("user@example.com")

	// 使用本地管理请求，确保邀请中的域名不会从请求 Host 推导。
	call := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://localhost:18080"+path, bytes.NewBufferString(body))
		req.Header.Set("Origin", "http://localhost:18080")
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		result := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(result, req)
		return result
	}
	for _, tt := range []struct {
		name   string
		cookie *http.Cookie
		want   int
	}{
		{"anonymous", nil, http.StatusUnauthorized},
		{"ordinary user", userCookie, http.StatusForbidden},
	} {
		result := call(http.MethodPost, "/api/users", `{"email":"blocked@example.com","sendInvitation":true}`, tt.cookie)
		if result.Code != tt.want || mailer.inviteCalls != 0 {
			t.Fatalf("%s invitation not blocked: %d %s", tt.name, result.Code, result.Body.String())
		}
	}
	for _, tt := range []struct {
		name, body, email string
		fail              bool
		want              generated.InvitationStatus
	}{
		{"omitted", `{"email":"default@example.com"}`, "default@example.com", false, generated.InvitationStatusNotRequested},
		{"false", `{"email":"false@example.com","sendInvitation":false}`, "false@example.com", false, generated.InvitationStatusNotRequested},
		{"true", `{"email":" INVITED@Example.com ","sendInvitation":true}`, "invited@example.com", false, generated.InvitationStatusSent},
		{"SMTP failure", `{"email":"failed@example.com","sendInvitation":true}`, "failed@example.com", true, generated.InvitationStatusFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mailer.inviteErr = nil
			if tt.fail {
				mailer.inviteErr = errors.New("private SMTP server transcript")
			}
			before := mailer.inviteCalls
			result := call(http.MethodPost, "/api/users", tt.body, adminCookie)
			var added generated.AddUserResponse
			if result.Code != http.StatusCreated || json.Unmarshal(result.Body.Bytes(), &added) != nil {
				t.Fatalf("add invitation user: %d %s", result.Code, result.Body.String())
			}
			if string(added.Email) != tt.email || added.Invitation != tt.want || added.Role != generated.AddUserResponseRoleUser {
				t.Fatalf("unexpected added result: %+v", added)
			}
			stored, err := store.FindUserByEmail(context.Background(), tt.email)
			if err != nil || stored.ID != added.Id {
				t.Fatalf("created user missing after invitation attempt: %+v %v", stored, err)
			}
			if tt.want == generated.InvitationStatusNotRequested {
				if mailer.inviteCalls != before {
					t.Fatal("default/false option sent an invitation")
				}
			} else if mailer.inviteCalls != before+1 || mailer.invites[tt.email] != "https://stack.example.com/login" {
				t.Fatalf("invitation destination changed or sent repeatedly: %+v", mailer.invites)
			}
			if mailer.sends[tt.email] != 0 || strings.Contains(result.Body.String(), "private SMTP") {
				t.Fatal("invitation generated a code or exposed internal SMTP error")
			}
		})
	}
	before := mailer.inviteCalls
	for _, tt := range []struct {
		body string
		want int
	}{
		{`{"email":"invited@example.com","sendInvitation":true}`, http.StatusConflict},
		{`{"email":"not-an-email","sendInvitation":true}`, http.StatusBadRequest},
		{`{"email":"bad-option@example.com","sendInvitation":"true"}`, http.StatusBadRequest},
		{`{"email":"custom-url@example.com","sendInvitation":true,"loginUrl":"https://evil.example"}`, http.StatusBadRequest},
	} {
		result := call(http.MethodPost, "/api/users", tt.body, adminCookie)
		if result.Code != tt.want {
			t.Fatalf("invalid/duplicate invitation status: %d %s", result.Code, result.Body.String())
		}
	}
	if mailer.inviteCalls != before {
		t.Fatal("invalid or duplicate request sent another invitation")
	}
	var challenges int
	if err := db.QueryRow("SELECT count(*) FROM auth_challenges").Scan(&challenges); err != nil || challenges != 0 {
		t.Fatalf("invitation created a login challenge: %d %v", challenges, err)
	}
	// 正常邀请和发送失败的邮箱都可以走既有的验证码登录流程。
	for _, email := range []string{"invited@example.com", "failed@example.com"} {
		cookie := login(email)
		if result := call(http.MethodGet, "/api/auth/session", "", cookie); result.Code != http.StatusOK {
			t.Fatalf("invited user cannot log in: %d %s", result.Code, result.Body.String())
		}
	}
	var listed generated.UserListResponse
	result := call(http.MethodGet, "/api/users", "", adminCookie)
	if json.Unmarshal(result.Body.Bytes(), &listed) != nil || listed.InvitationLoginUrl == nil || *listed.InvitationLoginUrl != server.config.InvitationLoginURL {
		t.Fatalf("missing invitation URL in admin list: %s", result.Body.String())
	}
	for _, invalidURL := range []string{"", "http://localhost:18080/login"} {
		server.config.InvitationLoginURL = invalidURL
		result := call(http.MethodPost, "/api/users", `{"email":"unconfigured@example.com","sendInvitation":true}`, adminCookie)
		if result.Code != http.StatusServiceUnavailable {
			t.Fatalf("unconfigured invitation accepted: %d %s", result.Code, result.Body.String())
		}
		if _, err := store.FindUserByEmail(context.Background(), "unconfigured@example.com"); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("invalid config added an email: %v", err)
		}
		result = call(http.MethodGet, "/api/users", "", adminCookie)
		if strings.Contains(result.Body.String(), "invitationLoginUrl") {
			t.Fatal("invalid invitation URL exposed to UI")
		}
	}
	if result := call(http.MethodPost, "/api/users", `{"email":"without-url@example.com"}`, adminCookie); result.Code != http.StatusCreated {
		t.Fatalf("default add requires invitation config: %d %s", result.Code, result.Body.String())
	}
	if mailer.inviteCalls != before {
		t.Fatal("unconfigured invitation contacted mailer")
	}
}
