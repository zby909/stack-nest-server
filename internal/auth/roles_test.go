package auth

import (
	"context"
	"testing"
)

// TestSuperAdminKeepsAdminPermissions 覆盖角色继承并确保邮箱配置不能提升普通用户。
func TestSuperAdminKeepsAdminPermissions(t *testing.T) {
	if !IsAdmin(RoleSuperAdmin) || !IsAdmin(RoleAdmin) || IsAdmin(RoleUser) {
		t.Fatal("role inheritance is incorrect")
	}
	s := &Service{superAdminEmail: "owner@example.com"}
	for _, base := range []User{{Email: "owner@example.com", Role: RoleAdmin}, {Email: "other@example.com", Role: RoleAdmin}, {Email: "owner@example.com", Role: RoleUser}} {
		result := s.effectiveUser(base)
		if (result.Role == RoleSuperAdmin) != (base.Email == "owner@example.com" && base.Role == RoleAdmin) {
			t.Fatal("incorrect elevation")
		}
	}
}

type superAdminStore struct {
	Store
	role string
}

// FindUserByEmail 仅向身份配置测试提供已有账户。
func (s superAdminStore) FindUserByEmail(_ context.Context, email string) (User, error) {
	if email != "owner@example.com" {
		return User{}, ErrNotFound
	}
	return User{Email: email, Role: s.role}, nil
}

// TestSuperAdminRequiresExistingAdministrator 防止部署参数误将普通用户或未知邮箱提升。
func TestSuperAdminRequiresExistingAdministrator(t *testing.T) {
	for _, role := range []string{RoleUser, RoleAdmin, RoleSuperAdmin} {
		service := &Service{store: superAdminStore{role: role}}
		err := service.SetSuperAdmin(context.Background(), " OWNER@example.com ")
		if (err == nil) != (role == RoleAdmin) {
			t.Fatalf("unexpected elevation result for %s: %v", role, err)
		}
		if err != nil && service.superAdminEmail != "" {
			t.Fatal("failed elevation changed identity")
		}
		if role == RoleAdmin && service.superAdminEmail != "owner@example.com" {
			t.Fatal("identity was not normalized")
		}
	}
	service := &Service{store: superAdminStore{role: RoleAdmin}}
	if service.SetSuperAdmin(context.Background(), "unknown@example.com") == nil {
		t.Fatal("unknown account accepted")
	}
}
