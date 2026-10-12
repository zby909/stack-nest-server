package httpserver

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/egress"
)

type networkAuthStore struct {
	auth.Store
	actor auth.User
}

// FindUserByEmail 只返回部署者基础管理员身份，超管由认证服务推导。
func (s networkAuthStore) FindUserByEmail(_ context.Context, email string) (auth.User, error) {
	if email == "owner@example.com" {
		return auth.User{ID: "owner", Email: email, Role: auth.RoleAdmin}, nil
	}
	return auth.User{}, auth.ErrNotFound
}

// FindSession 为权限边界测试提供当前数据库角色。
func (s networkAuthStore) FindSession(context.Context, string, time.Time) (auth.User, error) {
	return s.actor, nil
}

type networkControlStub struct {
	writes int
	err    error
	input  egress.Update
}

// Get 返回宿主配置，不访问真实防火墙。
func (s *networkControlStub) Get(context.Context) (egress.View, error) {
	return egress.View{Revision: "current", Ready: true}, nil
}

// Update 捕获确认后的写入并模拟并发冲突。
func (s *networkControlStub) Update(_ context.Context, input egress.Update) (egress.View, error) {
	s.writes++
	s.input = input
	return egress.View{Ready: true}, s.err
}

// TestNetworkRoleBoundary 验证普通用户和普通管理员无法读取或修改宿主策略。
func TestNetworkRoleBoundary(t *testing.T) {
	body := `{"config":{"lanInterfaces":["eth0"],"blockedCIDRs":["192.168.2.0/24"],"allow":[],"dnsServers":["1.1.1.1"]},"expectedRevision":"current"}`
	for _, actor := range []auth.User{{ID: "u", Email: "user@example.com", Role: auth.RoleUser}, {ID: "a", Email: "admin@example.com", Role: auth.RoleAdmin}, {ID: "owner", Email: "owner@example.com", Role: auth.RoleAdmin}} {
		control := &networkControlStub{}
		service, err := auth.NewService(networkAuthStore{actor: actor}, &testMailer{}, "test-secret-at-least-thirty-two-characters")
		if err != nil {
			t.Fatal(err)
		}
		if err = service.SetSuperAdmin(context.Background(), "owner@example.com"); err != nil {
			t.Fatal(err)
		}
		server := New(Config{Network: control, SitesDir: t.TempDir()}, nil, service)
		for _, method := range []string{"GET", "PUT"} {
			req := httptest.NewRequest(method, "http://localhost/api/network", bytes.NewBufferString(body))
			req.Header.Set("Origin", "http://localhost")
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: strings.Repeat("a", 43)})
			result := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(result, req)
			expected := 403
			if actor.ID == "owner" {
				expected = 200
			}
			if result.Code != expected {
				t.Fatalf("%s %s: status %d body %s", actor.ID, method, result.Code, result.Body.String())
			}
		}
		if actor.ID != "owner" && control.writes != 0 {
			t.Fatal("unauthorized mutation reached control service")
		}
		if actor.ID == "owner" && control.writes != 1 {
			t.Fatal("superadmin failed to update")
		}
	}
}

// TestNetworkMutationProtection 验证跨站、非法配置和版本冲突不会被当成保存成功。
func TestNetworkMutationProtection(t *testing.T) {
	control := &networkControlStub{err: egress.ErrConflict}
	service, _ := auth.NewService(networkAuthStore{actor: auth.User{ID: "owner", Email: "owner@example.com", Role: auth.RoleAdmin}}, &testMailer{}, "test-secret-at-least-thirty-two-characters")
	if err := service.SetSuperAdmin(context.Background(), "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	server := New(Config{Network: control, SitesDir: t.TempDir()}, nil, service)
	tests := []struct {
		origin, body string
		status       int
	}{
		{"https://evil.example", `{}`, 403},
		{"http://localhost", `{"config":{"lanInterfaces":["eth0"],"blockedCIDRs":["0.0.0.0/0"],"allow":[],"dnsServers":["1.1.1.1"]},"expectedRevision":"current"}`, 400},
		{"http://localhost", `{"config":{"lanInterfaces":["eth0"],"blockedCIDRs":[],"allow":[],"dnsServers":["1.1.1.1"]},"expectedRevision":"old"}`, 409},
	}
	for _, tc := range tests {
		req := httptest.NewRequest("PUT", "http://localhost/api/network", strings.NewReader(tc.body))
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: strings.Repeat("a", 43)})
		result := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(result, req)
		if result.Code != tc.status {
			t.Fatalf("wanted %d got %d: %s", tc.status, result.Code, result.Body.String())
		}
	}
	if control.writes != 1 || !errors.Is(control.err, egress.ErrConflict) {
		t.Fatal("rejected request reached host service")
	}
}
