package postgres

import (
	"context"
	"database/sql"
	"errors"
	"stacknest/server/internal/auth"
)

// ChangeUserRole 锁定目标账户后同时变更角色并撤销验证码和所有会话。
func (s *Store) ChangeUserRole(ctx context.Context, id, expected, role, protectedEmail, bootstrapEmail string) (auth.User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return auth.User{}, err
	}
	defer tx.Rollback()
	var user auth.User
	err = tx.QueryRowContext(ctx, "SELECT id,email,role,created_at FROM auth_users WHERE id=$1 FOR UPDATE", id).Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return user, auth.ErrNotFound
	}
	if err != nil {
		return user, err
	}
	if user.Email == protectedEmail || user.Email == bootstrapEmail || (role != auth.RoleAdmin && role != auth.RoleUser) {
		return user, auth.ErrForbidden
	}
	if user.Role != expected || role == expected {
		return user, auth.ErrRoleConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE auth_users SET role=$1 WHERE id=$2", role, id); err != nil {
		return user, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM auth_challenges WHERE user_id=$1", id); err != nil {
		return user, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM auth_sessions WHERE user_id=$1", id); err != nil {
		return user, err
	}
	user.Role = role
	return user, tx.Commit()
}
