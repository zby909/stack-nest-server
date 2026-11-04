package postgres

import (
	"context"
	"database/sql"
	"errors"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/deploytoken"
)

var _ deploytoken.AppStore = (*Store)(nil)

// tokenApp 在应用锁内重新检查用户权限，复用创建、列表和撤销的授权边界。
func tokenApp(ctx context.Context, tx *sql.Tx, id string, actor auth.User) (string, error) {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT a.owner_user_id FROM container_apps a JOIN auth_users u ON u.id=$2
		WHERE a.id=$1 AND (a.owner_user_id=u.id OR u.role IN ('admin','superadmin')) FOR UPDATE OF a`, id, actor.ID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", containerapp.ErrNotFound
	}
	return owner, err
}

// CreateAppDeployToken 只保存摘要，应用归属和创建者身份取自当前数据库。
func (s *Store) CreateAppDeployToken(ctx context.Context, value deploytoken.Token, actor auth.User) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	owner, err := tokenApp(ctx, tx, value.AppID, actor)
	if err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM deploy_tokens WHERE app_id=$1", value.AppID).Scan(&count); err != nil {
		return err
	}
	if count >= deploytoken.MaxTokens {
		return deploytoken.ErrLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO deploy_tokens (id,app_id,owner_user_id,creator_user_id,name,token_hash,can_publish,created_at,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,false,$7,$8)`, value.ID, value.AppID, owner, actor.ID, value.Name, value.Hash, value.CreatedAt, value.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ListAppDeployTokens 不返回摘要、原文和内部归属字段。
func (s *Store) ListAppDeployTokens(ctx context.Context, id string, actor auth.User) ([]deploytoken.Token, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tokenApp(ctx, tx, id, actor); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.name,t.created_at,t.expires_at,t.last_used_at,u.email
		FROM deploy_tokens t JOIN auth_users u ON u.id=t.creator_user_id WHERE t.app_id=$1 ORDER BY t.created_at DESC,t.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []deploytoken.Token{}
	for rows.Next() {
		var value deploytoken.Token
		if err = rows.Scan(&value.ID, &value.Name, &value.CreatedAt, &value.ExpiresAt, &value.LastUsedAt, &value.CreatedBy); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// DeleteAppDeployToken 重复撤销幂等；已入队任务不会因撤销凭据而被自动取消。
func (s *Store) DeleteAppDeployToken(ctx context.Context, appID, id string, actor auth.User) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tokenApp(ctx, tx, appID, actor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM deploy_tokens WHERE app_id=$1 AND id=$2", appID, id); err != nil {
		return err
	}
	return tx.Commit()
}
