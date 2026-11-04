package postgres

import (
	"context"
	"database/sql"
	"errors"

	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/deploytoken"
)

// appDeploymentGrant 在提交事务内重新校验撤销、过期、角色和归属，不依赖 HTTP 早先的授权。
func appDeploymentGrant(ctx context.Context, tx *sql.Tx, grant deploytoken.Grant, owner string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT t.id FROM deploy_tokens t JOIN auth_users u ON u.id=t.creator_user_id
		WHERE t.id=$1 AND t.app_id=$2 AND t.owner_user_id=$3 AND t.expires_at>now() AND u.id=$4
		AND (u.id=$3 OR u.role IN ('admin','superadmin')) FOR SHARE OF t,u`, grant.ID, grant.AppID, owner, grant.User.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return deploytoken.ErrInvalid
	}
	return err
}

// SaveAppImage 原子保存镜像任务及有界回执，相同请求仅返回原代次，绝不重新部署。
func (s *Store) SaveAppImage(ctx context.Context, a containerapp.App, grant deploytoken.Grant, input containerapp.ImageUpdate) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var generation int64
	var owner, status string
	err = tx.QueryRowContext(ctx, "SELECT generation,owner_user_id,status FROM container_apps WHERE id=$1 FOR UPDATE", grant.AppID).Scan(&generation, &owner, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, containerapp.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if owner != grant.OwnerID || a.ID != grant.AppID {
		return 0, deploytoken.ErrInvalid
	}
	if err = appDeploymentGrant(ctx, tx, grant, owner); err != nil {
		return 0, err
	}
	var requestID, tokenID, image string
	var expected, result int64
	err = tx.QueryRowContext(ctx, "SELECT request_id::text,token_id,image,expected_generation,generation FROM app_deploy_requests WHERE app_id=$1", a.ID).Scan(&requestID, &tokenID, &image, &expected, &result)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil && requestID == input.RequestID {
		if tokenID != grant.ID || image != input.Image || expected != input.ExpectedGeneration || result != generation {
			return 0, containerapp.ErrConflict
		}
		return result, tx.Commit()
	}
	if generation != input.ExpectedGeneration || a.Generation != generation {
		return 0, containerapp.ErrConflict
	}
	if status == "pending" || status == "deploying" {
		return 0, containerapp.ErrBusy
	}
	_, err = tx.ExecContext(ctx, `UPDATE container_apps SET sealed=$2,generation=generation+1,action='deploy',status='pending',error='',actor_id=$3,actor_email=$4,updated_at=now() WHERE id=$1`, a.ID, a.Sealed, grant.User.ID, grant.User.Email)
	if err != nil {
		return 0, err
	}
	result = generation + 1
	_, err = tx.ExecContext(ctx, `INSERT INTO app_deploy_requests(app_id,token_id,request_id,image,expected_generation,generation) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(app_id) DO UPDATE SET token_id=EXCLUDED.token_id,request_id=EXCLUDED.request_id,image=EXCLUDED.image,expected_generation=EXCLUDED.expected_generation,generation=EXCLUDED.generation`, a.ID, grant.ID, input.RequestID, input.Image, generation, result)
	if err != nil {
		return 0, err
	}
	return result, tx.Commit()
}
