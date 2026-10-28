package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"stacknest/server/internal/deploytoken"
)

// checkTokenManagement 复用真实邮箱登录会话，验证默认权限、CSRF、跨用户拒绝及在线撤销。
func checkTokenManagement(t *testing.T, s *Server, owner, other *http.Cookie) {
	t.Helper()
	path := "/api/projects/ordinary-site/deploy-tokens"
	call := func(method, path, body string, cookie *http.Cookie, origin bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Host = "localhost:8080"
		r.Header.Set("Content-Type", "application/json")
		if origin {
			r.Header.Set("Origin", "http://localhost:8080")
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, r)
		return w
	}
	if w := call("POST", path, `{"name":"CI"}`, owner, false); w.Code != 403 {
		t.Fatal("credential creation bypassed CSRF", w.Code)
	}
	for _, method := range []string{"GET", "POST"} {
		if w := call(method, path, `{"name":"CI"}`, other, true); w.Code != 404 {
			t.Fatal("other user's credential access", method, w.Code)
		}
	}
	w := call("POST", path, `{"name":"CI"}`, owner, true)
	if w.Code != 201 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("create credential", w.Code, w.Body.String())
	}
	var result struct {
		Token  deploytoken.Token `json:"token"`
		Secret string            `json:"secret"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Secret == "" || result.Token.CanPublish {
		t.Fatal("invalid creation response", err)
	}
	if w = deployRequest(s, "GET", deployPrefix+"project", result.Secret, nil); w.Code != 200 {
		t.Fatal("new token rejected", w.Code)
	}
	w = call("GET", path, "", owner, true)
	if w.Code != 200 || strings.Contains(w.Body.String(), result.Secret) || strings.Contains(w.Body.String(), "token_hash") {
		t.Fatal("credential listing leaked secret")
	}
	if w = call("DELETE", path+"/"+result.Token.ID, "", other, true); w.Code != 404 {
		t.Fatal("other user revoked credential", w.Code)
	}
	if w = call("DELETE", path+"/"+result.Token.ID, "", owner, true); w.Code != 204 {
		t.Fatal("credential revoke", w.Code)
	}
	if w = deployRequest(s, "GET", deployPrefix+"project", result.Secret, nil); w.Code != 401 {
		t.Fatal("revoked credential accepted", w.Code)
	}
	if s.config.Activity != nil {
		events, err := s.config.Activity.List()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(events)
		if strings.Contains(string(data), result.Secret) {
			t.Fatal("credential in audit log")
		}
	}
}
