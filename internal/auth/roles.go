package auth

import (
	"context"
	"errors"
)

// RoleSuperAdmin 标记由部署配置指定的实例维护者，不允许通过普通邮箱接口授予。
const RoleSuperAdmin = "superadmin"

// IsAdmin 让超管继承项目、用户和服务器资源管理能力。
func IsAdmin(role string) bool { return role == RoleAdmin || role == RoleSuperAdmin }

// AddAdministrator 只有有效超管身份可以创建管理员，不能通过该接口授予超管。
func (s *Service) AddAdministrator(ctx context.Context, actor User, raw string) (User, error) {
	if actor.Role != RoleSuperAdmin || actor.Email != s.superAdminEmail || actor.ID == "" {
		return User{}, ErrForbidden
	}
	email, err := NormalizeEmail(raw)
	if err != nil {
		return User{}, err
	}
	return s.store.AddUser(ctx, email, RoleAdmin)
}

// ChangeUserRole 验证超管身份并阻止将任意用户提升为超管。
func (s *Service) ChangeUserRole(ctx context.Context, actor User, id, expected, role string) (User, error) {
	if actor.Role != RoleSuperAdmin || actor.Email != s.superAdminEmail || actor.ID == "" || actor.ID == id {
		return User{}, ErrForbidden
	}
	if (role != RoleAdmin && role != RoleUser) || (expected != RoleAdmin && expected != RoleUser) {
		return User{}, ErrForbidden
	}
	return s.store.ChangeUserRole(ctx, id, expected, role, s.superAdminEmail, s.bootstrapAdminEmail)
}

// SetSuperAdmin 只允许把已存在的管理员指定为超管，普通用户不能被配置直接提升。
func (s *Service) SetSuperAdmin(ctx context.Context, raw string) error {
	email, err := NormalizeEmail(raw)
	if err != nil {
		return err
	}
	user, err := s.store.FindUserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if user.Role != RoleAdmin {
		return errors.New("configured super administrator must be an existing administrator")
	}
	s.superAdminEmail = email
	return nil
}

// effectiveUser 在每次会话查询时从部署配置计算超管身份，数据库仍保存基础管理角色。
func (s *Service) effectiveUser(user User) User {
	if user.Role == RoleAdmin && user.Email == s.superAdminEmail {
		user.Role = RoleSuperAdmin
	}
	return user
}
