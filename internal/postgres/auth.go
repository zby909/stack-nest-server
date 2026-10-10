package postgres

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"stacknest/server/internal/auth"
)

var _ auth.Store = (*Store)(nil)

// BootstrapUsers 仅在用户表为空时原子地创建初始账号。
func (s *Store) BootstrapUsers(ctx context.Context, adminEmail, initialUserEmail string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin auth bootstrap: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(7090909)"); err != nil {
		return fmt.Errorf("lock auth bootstrap: %w", err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM auth_users").Scan(&count); err != nil {
		return fmt.Errorf("count auth users: %w", err)
	}
	if count == 0 {
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, "INSERT INTO auth_users (id,email,role,created_at) VALUES ($1,$2,$3,$4)", uuid.NewString(), adminEmail, auth.RoleAdmin, now); err != nil {
			return fmt.Errorf("create initial admin: %w", err)
		}
		if initialUserEmail != "" {
			if _, err := tx.ExecContext(ctx, "INSERT INTO auth_users (id,email,role,created_at) VALUES ($1,$2,$3,$4)", uuid.NewString(), initialUserEmail, auth.RoleUser, now); err != nil {
				return fmt.Errorf("create initial user: %w", err)
			}
		}
	} else {
		var role string
		if err := tx.QueryRowContext(ctx, "SELECT role FROM auth_users WHERE email=$1", adminEmail).Scan(&role); err != nil || role != auth.RoleAdmin {
			return errors.New("configured admin email does not match the existing admin")
		}
	}
	return tx.Commit()
}

// FindUserByEmail 按规范化的邮箱地址查询用户。
func (s *Store) FindUserByEmail(ctx context.Context, email string) (auth.User, error) {
	var user auth.User
	err := s.db.QueryRowContext(ctx, "SELECT id,email,role,created_at FROM auth_users WHERE email=$1", email).Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("find auth user: %w", err)
	}
	return user, nil
}

// ListUsers 返回登录白名单及角色。
func (s *Store) ListUsers(ctx context.Context) ([]auth.User, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,email,role,created_at FROM auth_users ORDER BY created_at,id")
	if err != nil {
		return nil, fmt.Errorf("list auth users: %w", err)
	}
	defer rows.Close()
	users := make([]auth.User, 0)
	for rows.Next() {
		var user auth.User
		if err := rows.Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan auth user: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate auth users: %w", err)
	}
	return users, nil
}

// AddUser 添加一个普通用户邮箱。
func (s *Store) AddUser(ctx context.Context, email string) (auth.User, error) {
	user := auth.User{ID: uuid.NewString(), Email: email, Role: auth.RoleUser, CreatedAt: time.Now().UTC()}
	_, err := s.db.ExecContext(ctx, "INSERT INTO auth_users (id,email,role,created_at) VALUES ($1,$2,$3,$4)", user.ID, user.Email, user.Role, user.CreatedAt)
	if uniqueViolation(err) {
		return auth.User{}, auth.ErrExists
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("add auth user: %w", err)
	}
	return user, nil
}

// DeleteUser 原子地将普通用户项目转交管理员，再删除用户并撤销其会话。
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete auth user: %w", err)
	}
	defer tx.Rollback()
	var role string
	err = tx.QueryRowContext(ctx, "SELECT role FROM auth_users WHERE id=$1 FOR UPDATE", id).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("find auth user before delete: %w", err)
	}
	if role != auth.RoleUser {
		return auth.ErrCannotDelete
	}
	var adminID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM auth_users WHERE role=$1 ORDER BY created_at,id LIMIT 1 FOR UPDATE", auth.RoleAdmin).Scan(&adminID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("cannot transfer projects: administrator is missing")
	}
	if err != nil {
		return fmt.Errorf("find administrator before transfer: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE projects SET owner_user_id=$1 WHERE owner_user_id=$2", adminID, id); err != nil {
		return fmt.Errorf("transfer user projects: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE container_apps SET owner_user_id=$1 WHERE owner_user_id=$2", adminID, id); err != nil {
		return fmt.Errorf("transfer user container apps: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM auth_users WHERE id=$1", id); err != nil {
		return fmt.Errorf("delete auth user: %w", err)
	}
	return tx.Commit()
}

// SaveChallenge 原子地应用每邮箱 60 秒发送冷却，并替换旧验证码。
func (s *Store) SaveChallenge(ctx context.Context, userID, codeHash string, sentAt, expiresAt time.Time) error {
	_, _ = s.db.ExecContext(ctx, "DELETE FROM auth_challenges WHERE expires_at < $1", sentAt)
	var id string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO auth_challenges (id,user_id,code_hash,attempts,sent_at,expires_at)
		VALUES ($1,$2,$3,0,$4,$5)
		ON CONFLICT (user_id) DO UPDATE SET code_hash=EXCLUDED.code_hash, attempts=0,
			sent_at=EXCLUDED.sent_at, expires_at=EXCLUDED.expires_at
		WHERE auth_challenges.sent_at <= $6
		RETURNING id`, uuid.NewString(), userID, codeHash, sentAt, expiresAt, sentAt.Add(-time.Minute)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.ErrRateLimited
	}
	if err != nil {
		return fmt.Errorf("save auth challenge: %w", err)
	}
	return nil
}

// DiscardChallenge 在发信失败后清除本次验证码。
func (s *Store) DiscardChallenge(ctx context.Context, userID, codeHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM auth_challenges WHERE user_id=$1 AND code_hash=$2", userID, codeHash)
	return err
}

// ExchangeChallenge 在事务中校验并消费验证码，再写入会话。
func (s *Store) ExchangeChallenge(ctx context.Context, userID, codeHash, tokenHash string, now, sessionExpiresAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin auth verification: %w", err)
	}
	defer tx.Rollback()
	var storedHash string
	var attempts int
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, "SELECT code_hash,attempts,expires_at FROM auth_challenges WHERE user_id=$1 FOR UPDATE", userID).Scan(&storedHash, &attempts, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.ErrInvalidCode
	}
	if err != nil {
		return fmt.Errorf("read auth challenge: %w", err)
	}
	if !now.Before(expiresAt) || attempts >= 5 {
		if !now.Before(expiresAt) {
			if _, err := tx.ExecContext(ctx, "DELETE FROM auth_challenges WHERE user_id=$1", userID); err != nil {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return auth.ErrInvalidCode
	}
	if subtle.ConstantTimeCompare([]byte(storedHash), []byte(codeHash)) != 1 {
		_, err = tx.ExecContext(ctx, "UPDATE auth_challenges SET attempts=attempts+1 WHERE user_id=$1", userID)
		if err != nil {
			return fmt.Errorf("increment auth attempts: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return auth.ErrInvalidCode
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM auth_challenges WHERE user_id=$1", userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO auth_sessions (id,user_id,created_at,expires_at) VALUES ($1,$2,$3,$4)", tokenHash, userID, now, sessionExpiresAt); err != nil {
		return fmt.Errorf("create auth session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit auth verification: %w", err)
	}
	_, _ = s.db.ExecContext(ctx, "DELETE FROM auth_sessions WHERE expires_at < $1", now)
	return nil
}

// FindSession 仅返回尚未过期且用户仍在白名单中的会话。
func (s *Store) FindSession(ctx context.Context, tokenHash string, now time.Time) (auth.User, error) {
	var user auth.User
	err := s.db.QueryRowContext(ctx, `SELECT u.id,u.email,u.role,u.created_at
		FROM auth_sessions s JOIN auth_users u ON u.id=s.user_id
		WHERE s.id=$1 AND s.expires_at>$2`, tokenHash, now).Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("find auth session: %w", err)
	}
	return user, nil
}

// DeleteSession 撤销一个会话。
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM auth_sessions WHERE id=$1", tokenHash)
	return err
}

// uniqueViolation 识别 PostgreSQL 唯一约束冲突。
func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
