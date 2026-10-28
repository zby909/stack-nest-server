package postgres

import (
	"context"
	"database/sql"
	"errors"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/deploytoken"
	"stacknest/server/internal/project"
)

var _ deploytoken.Store = (*Store)(nil)

// tokenProject 锁定目标项目并从数据库重新验证操作者，避免过期页面授予权限。
func tokenProject(ctx context.Context, tx *sql.Tx, projectID string, actor auth.User) (string, error) {
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT p.owner_user_id FROM projects p JOIN auth_users u ON u.id=$2
		WHERE p.id=$1 AND (p.owner_user_id=u.id OR u.role IN ('admin','superadmin')) FOR UPDATE OF p`, projectID, actor.ID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", project.ErrNotFound
	}
	return owner, err
}

// CreateDeployToken 在项目锁内检查数量及当前所有者，不保存原始 Token。
func (s *Store) CreateDeployToken(ctx context.Context, value deploytoken.Token, actor auth.User) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	owner, err := tokenProject(ctx, tx, value.ProjectID, actor)
	if err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM deploy_tokens WHERE project_id=$1", value.ProjectID).Scan(&count); err != nil {
		return err
	}
	if count >= deploytoken.MaxTokens {
		return deploytoken.ErrLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO deploy_tokens (id,project_id,owner_user_id,creator_user_id,name,token_hash,can_publish,created_at,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, value.ID, value.ProjectID, owner, actor.ID, value.Name, value.Hash, value.CanPublish, value.CreatedAt, value.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ListDeployTokens 返回不含摘要的有界列表，失效凭据可以手动撤销清理。
func (s *Store) ListDeployTokens(ctx context.Context, projectID string, actor auth.User) ([]deploytoken.Token, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tokenProject(ctx, tx, projectID, actor); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.name,t.can_publish,t.created_at,t.expires_at,t.last_used_at,u.email
		FROM deploy_tokens t JOIN auth_users u ON u.id=t.creator_user_id WHERE t.project_id=$1 ORDER BY t.created_at DESC,t.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]deploytoken.Token, 0)
	for rows.Next() {
		var v deploytoken.Token
		if err = rows.Scan(&v.ID, &v.Name, &v.CanPublish, &v.CreatedAt, &v.ExpiresAt, &v.LastUsedAt, &v.CreatedBy); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// DeleteDeployToken 即时撤销后续请求；重复撤销保持幂等。
func (s *Store) DeleteDeployToken(ctx context.Context, projectID, id string, actor auth.User) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tokenProject(ctx, tx, projectID, actor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM deploy_tokens WHERE project_id=$1 AND id=$2", projectID, id); err != nil {
		return err
	}
	return tx.Commit()
}

// AuthenticateDeployToken 每次检查过期、归属及创建者现有权限，使用时间最多每分钟写一次。
func (s *Store) AuthenticateDeployToken(ctx context.Context, hash string) (deploytoken.Grant, error) {
	var g deploytoken.Grant
	err := s.db.QueryRowContext(ctx, `SELECT t.id,t.name,t.can_publish,t.project_id,t.owner_user_id,p.slug,u.id,u.email,u.role,u.created_at
		FROM deploy_tokens t JOIN projects p ON p.id=t.project_id JOIN auth_users u ON u.id=t.creator_user_id
		WHERE t.token_hash=$1 AND t.expires_at>now() AND p.owner_user_id=t.owner_user_id
		AND (u.id=p.owner_user_id OR u.role IN ('admin','superadmin'))`, hash).
		Scan(&g.ID, &g.Name, &g.CanPublish, &g.ProjectID, &g.OwnerID, &g.Slug, &g.User.ID, &g.User.Email, &g.User.Role, &g.User.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return g, deploytoken.ErrInvalid
	}
	if err != nil {
		return g, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE deploy_tokens SET last_used_at=now() WHERE id=$1 AND (last_used_at IS NULL OR last_used_at<now()-interval '1 minute')`, g.ID)
	return g, err
}
