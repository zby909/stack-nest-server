package httpserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/deployclient"
	"stacknest/server/internal/deploytoken"
	"stacknest/server/internal/postgres"
	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

type automationRuntime struct {
	appTestRuntime
	operations chan containerapp.Operation
}

// Operate 记录实际收到的配置，模拟候选失败和旧容器恢复，不接触 Docker。
func (r automationRuntime) Operate(_ context.Context, op containerapp.Operation) (containerapp.Result, error) {
	r.operations <- op
	result := containerapp.Result{State: "running", ContainerID: "aaaaaaaaaaaa", ImageID: "image-fixture"}
	if strings.HasSuffix(op.Spec.Image, ":bad") {
		return result, errors.New("健康检查失败 secret-app-value registry-secret-value")
	}
	return result, nil
}

// TestAppAutomationIntegration 使用真实数据库、邮箱会话、HTTP、任务队列和 CLI 验证完整授权与幂等流程。
func TestAppAutomationIntegration(t *testing.T) {
	databaseURL := os.Getenv("STACKNEST_TEST_AUTH_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("STACKNEST_TEST_AUTH_DATABASE_URL is not set")
	}
	if !strings.Contains(databaseURL, "/stacknest_auth_test") {
		t.Fatal("dedicated auth test database required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	secret := "integration-fixture-with-at-least-thirty-two-bytes"
	mailer := &testMailer{codes: map[string]string{}, sends: map[string]int{}}
	login, err := auth.NewService(store, mailer, secret)
	if err != nil {
		t.Fatal(err)
	}
	if err = login.BootstrapUsers(ctx, "admin@example.com", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	owner, _ := store.FindUserByEmail(ctx, "owner@example.com")
	admin, _ := store.FindUserByEmail(ctx, "admin@example.com")
	other, err := login.AddUser(ctx, "other@example.com")
	if err != nil {
		t.Fatal(err)
	}
	runtime := automationRuntime{operations: make(chan containerapp.Operation, 10)}
	root := t.TempDir()
	apps := containerapp.New(store, runtime, secret, storage.New(root), nil)
	actor := containerapp.Actor{ID: owner.ID, Email: owner.Email}
	input := containerapp.Input{Name: "CI app", Slug: "ci-app", Spec: containerapp.Spec{Image: "example/app:v1", Port: 3000, MemoryMiB: 128, CPUs: 0.5, DataPath: "/app/data", HealthPath: "/health", Env: map[string]string{"SECRET": "secret-app-value"}, Registry: &containerapp.Registry{Username: "fixture-user", Password: "registry-secret-value"}}}
	app, err := apps.Save(ctx, "", actor, input, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE container_apps SET status='ready',action='',active_sealed=sealed WHERE id=$1", app.ID); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Apps: apps, DeployTokens: store, AppDeployTokens: store, SitesDir: root, WebDir: root}, nil, login)
	// session 经邮箱验证码创建真实会话。
	session := func(user auth.User) *http.Cookie {
		t.Helper()
		if err := login.RequestCode(ctx, user.Email); err != nil {
			t.Fatal(err)
		}
		_, plain, err := login.VerifyCode(ctx, user.Email, mailer.codes[user.Email])
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: sessionCookie, Value: plain}
	}
	ownerCookie, otherCookie := session(owner), session(other)
	// manage 保留同源检查，凭据管理不能由 Bearer 或其他项目用户调用。
	manage := func(method, path, body string, cookie *http.Cookie, origin bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "localhost:8080"
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		if origin {
			r.Header.Set("Origin", "http://localhost:8080")
		}
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, r)
		return w
	}
	for _, check := range []struct {
		cookie *http.Cookie
		status int
	}{{ownerCookie, 200}, {otherCookie, 404}, {nil, 401}} {
		out := manage("GET", "/api/apps/"+app.ID+"/deployments", "", check.cookie, true)
		if out.Code != check.status {
			t.Fatal("history authorization", out.Code, out.Body.String())
		}
	}
	path := "/api/apps/" + app.ID + "/deploy-tokens"
	for _, check := range []struct {
		method string
		cookie *http.Cookie
		origin bool
		code   int
	}{
		{"GET", nil, true, 401}, {"GET", otherCookie, true, 404}, {"POST", otherCookie, true, 404}, {"POST", ownerCookie, false, 403},
	} {
		if w := manage(check.method, path, `{"name":"CI"}`, check.cookie, check.origin); w.Code != check.code {
			t.Fatalf("management boundary %s: %d %s", check.method, w.Code, w.Body.String())
		}
	}
	created := manage("POST", path, `{"name":"CI"}`, ownerCookie, true)
	var credential struct {
		Token  deploytoken.Token
		Secret string
	}
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &credential) != nil || credential.Secret == "" || credential.Token.CanPublish {
		t.Fatal("create token", created.Code, created.Body.String())
	}
	plain := credential.Secret
	hash, _ := deploytoken.Hash(plain)
	grant, err := store.AuthenticateDeployToken(ctx, hash)
	if err != nil || grant.AppID != app.ID || grant.ProjectID != "" {
		t.Fatal("invalid app grant", err)
	}
	if w := manage("GET", path, "", ownerCookie, true); w.Code != 200 || strings.Contains(w.Body.String(), plain) || strings.Contains(w.Body.String(), hash) {
		t.Fatal("token metadata leaked", w.Body.String())
	}
	if w := deployRequest(s, "GET", appDeployPrefix+"app", plain, nil); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "secret-app-value") {
		t.Fatal("status", w.Code, w.Body.String())
	}
	for _, path := range []string{deployPrefix + "project", "/api/apps/" + app.ID, "/api/network"} {
		if w := deployRequest(s, "GET", path, plain, nil); w.Code != 401 {
			t.Fatal("app token escaped", path, w.Code)
		}
	}
	p := project.Project{ID: "static-fixture", OwnerID: owner.ID, Name: "Static", Slug: "static-fixture", Status: "draft", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = store.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	staticToken, staticPlain, _ := deploytoken.New(p.ID, "Static", 30, false)
	if err = store.CreateDeployToken(ctx, staticToken, owner); err != nil {
		t.Fatal(err)
	}
	if w := deployRequest(s, "GET", appDeployPrefix+"app", staticPlain, nil); w.Code != 401 {
		t.Fatal("static token deployed app", w.Code)
	}
	for _, body := range []string{
		`{"image":"different/app:v2","expectedGeneration":1,"requestId":"` + uuid.NewString() + `"}`,
		`{"image":"example/app","expectedGeneration":1,"requestId":"` + uuid.NewString() + `"}`,
		`{"image":"example/app:v2","expectedGeneration":1,"requestId":"bad"}`,
		`{"image":"example/app:v2","expectedGeneration":1,"requestId":"` + uuid.NewString() + `","env":{"SECRET":"changed"}}`,
		`{"image":"example/app:v2","expectedGeneration":1,"requestId":"` + uuid.NewString() + `","appId":"` + uuid.NewString() + `"}`,
	} {
		if w := deployRequest(s, "POST", appDeployPrefix+"deploy", plain, []byte(body)); w.Code != 400 {
			t.Fatal("unsafe update accepted", w.Code, w.Body.String())
		}
	}
	request := containerapp.ImageUpdate{Image: "example/app:v2", ExpectedGeneration: 1, RequestID: uuid.NewString()}
	body, _ := json.Marshal(request)
	var wait sync.WaitGroup
	for range 5 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			w := deployRequest(s, "POST", appDeployPrefix+"deploy", plain, body)
			if w.Code != 202 {
				t.Errorf("duplicate POST: %d %s", w.Code, w.Body.String())
			}
		}()
	}
	wait.Wait()
	updated, err := apps.Get(ctx, app.ID, actor)
	if err != nil || updated.Generation != 2 || updated.Image != "example/app:v2" {
		t.Fatal("duplicate queue", updated.Generation, err)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); apps.Run(workerCtx) }()
	defer func() { stopWorker(); <-done }()
	// waitStatus 等待后台完成指定状态，不使用固定长等待。
	waitStatus := func(status string) {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			current, e := apps.Get(ctx, app.ID, actor)
			if e == nil && current.Status == status {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("worker did not reach", status)
	}
	waitStatus("ready")
	op := <-runtime.operations
	if op.Spec.Port != 3000 || op.Spec.MemoryMiB != 128 || op.Spec.CPUs != 0.5 || op.Spec.DataPath != "/app/data" || op.Spec.HealthPath != "/health" || op.Spec.Env["SECRET"] != "secret-app-value" || op.Spec.Registry.Password != "registry-secret-value" {
		t.Fatal("existing config changed")
	}
	if w := deployRequest(s, "POST", appDeployPrefix+"deploy", plain, body); w.Code != 202 {
		t.Fatal("completed request replay failed", w.Code)
	}
	history, err := apps.History(ctx, app.ID, actor)
	if err != nil || len(history) != 2 || history[0].Generation != 2 || history[0].Source != "automation" || history[0].Status != "succeeded" || history[0].Image != request.Image {
		t.Fatal("automation history duplicated or incorrect", history, err)
	}
	request.RequestID = uuid.NewString()
	body, _ = json.Marshal(request)
	if w := deployRequest(s, "POST", appDeployPrefix+"deploy", plain, body); w.Code != 409 {
		t.Fatal("stale baseline overwritten", w.Code)
	}
	httpServer := httptest.NewServer(s.http.Handler)
	defer httpServer.Close()
	t.Setenv("STACKNEST_URL", httpServer.URL)
	t.Setenv("STACKNEST_DEPLOY_TOKEN", plain)
	var output, progress bytes.Buffer
	if code := deployclient.Main(ctx, []string{"app", "deploy", "--image", "example/app:bad", "--timeout", "15s"}, &output, &progress); code != 1 || !strings.Contains(output.String(), `"status": "error"`) || strings.Contains(output.String()+progress.String(), "secret-app-value") || strings.Contains(output.String()+progress.String(), "registry-secret-value") {
		t.Fatal("CLI failure reporting", code, output.String(), progress.String())
	}
	<-runtime.operations
	output.Reset()
	progress.Reset()
	if code := deployclient.Main(ctx, []string{"app", "wait", "--generation", "2", "--timeout", "1s"}, &output, &progress); code != 1 {
		t.Fatal("CLI followed later generation", code)
	}
	output.Reset()
	progress.Reset()
	if code := deployclient.Main(ctx, []string{"app", "deploy", "--image", "example/app:v3", "--timeout", "15s"}, &output, &progress); code != 0 || !strings.Contains(output.String(), `"status": "ready"`) || !strings.Contains(output.String(), `"generation": 4`) {
		t.Fatal("CLI successful readiness", code, output.String(), progress.String())
	}
	if op := <-runtime.operations; op.Spec.Image != "example/app:v3" || len(runtime.operations) != 0 {
		t.Fatal("unexpected extra deployment")
	}
	// 凭据在中间件检查后失效时，提交事务仍必须拒绝。
	if _, err = db.Exec("UPDATE deploy_tokens SET expires_at=now()-interval '1 second' WHERE id=$1", credential.Token.ID); err != nil {
		t.Fatal(err)
	}
	latest, _ := apps.Get(ctx, app.ID, actor)
	if _, err = store.SaveAppImage(ctx, latest, grant, containerapp.ImageUpdate{Image: "example/app:v3", ExpectedGeneration: latest.Generation, RequestID: uuid.NewString()}); !errors.Is(err, deploytoken.ErrInvalid) {
		t.Fatal("expired grant raced transaction", err)
	}
	if w := deployRequest(s, "GET", appDeployPrefix+"app", plain, nil); w.Code != 401 {
		t.Fatal("expired token accepted", w.Code)
	}
	if w := manage("DELETE", path+"/"+credential.Token.ID, "", ownerCookie, true); w.Code != 204 {
		t.Fatal("revoke", w.Code)
	}
	if w := deployRequest(s, "GET", appDeployPrefix+"app", plain, nil); w.Code != 401 {
		t.Fatal("revoked token accepted", w.Code)
	}
	adminToken, _, _ := deploytoken.NewApp(app.ID, "Admin CI", 30)
	if err = store.CreateAppDeployToken(ctx, adminToken, admin); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE auth_users SET role='user' WHERE id=$1", admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AuthenticateDeployToken(ctx, adminToken.Hash); !errors.Is(err, deploytoken.ErrInvalid) {
		t.Fatal("demoted admin grant accepted", err)
	}
	ownerToken, _, _ := deploytoken.NewApp(app.ID, "Owner CI", 30)
	if err = store.CreateAppDeployToken(ctx, ownerToken, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE container_apps SET owner_user_id=$2 WHERE id=$1", app.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AuthenticateDeployToken(ctx, ownerToken.Hash); !errors.Is(err, deploytoken.ErrInvalid) {
		t.Fatal("transferred application grant accepted", err)
	}
}
