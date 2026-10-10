package postgres

import (
	"context"
	"database/sql"
	"errors"

	"stacknest/server/internal/project"
)

// SaveDraft 在项目行锁内检查正式版和唯一草稿，避免并发上传覆盖预览。
func (s *Store) SaveDraft(ctx context.Context, slug, ownerID, expectedID string, next project.Deployment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, owner string
	err = tx.QueryRowContext(ctx, "SELECT id,owner_user_id FROM projects WHERE slug=$1 FOR UPDATE", slug).Scan(&id, &owner)
	if errors.Is(err, sql.ErrNoRows) || err == nil && owner != ownerID {
		return project.ErrNotFound
	}
	if err != nil {
		return err
	}
	var current string
	err = tx.QueryRowContext(ctx, "SELECT id FROM deployments WHERE project_id=$1 AND status<>'draft' ORDER BY created_at DESC,id DESC LIMIT 1", id).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current != expectedID {
		return project.ErrVersionConflict
	}
	var hasDraft bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM deployments WHERE project_id=$1 AND status='draft')", id).Scan(&hasDraft); err != nil {
		return err
	}
	if hasDraft {
		return project.ErrDraftExists
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO deployments (id,project_id,status,file_name,created_at,finished_at,error,notes) VALUES ($1,$2,'draft',$3,$4,$4,'',$5)", next.ID, id, next.FileName, next.CreatedAt, next.Notes); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE projects SET updated_at=$1 WHERE id=$2", next.CreatedAt, id); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteDraft 只删除当前项目仍处于待发布状态的指定版本。
func (s *Store) DeleteDraft(ctx context.Context, slug, ownerID, draftID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE slug=$1 AND owner_user_id=$2 FOR UPDATE", slug, ownerID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return project.ErrNotFound
	}
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM deployments WHERE project_id=$1 AND id=$2 AND status='draft'", id, draftID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return project.ErrVersionConflict
	}
	return tx.Commit()
}
