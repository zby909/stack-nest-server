package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"stacknest/server/internal/project"
)

// ReplaceDeployment 锁定项目并比较当前部署，只保留与磁盘对应的当前版和上一版记录。
func (s *Store) ReplaceDeployment(ctx context.Context, slug, ownerID, expectedID string, next project.Deployment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, owner string
	err = tx.QueryRowContext(ctx, "SELECT id,owner_user_id FROM projects WHERE slug=$1 FOR UPDATE", slug).Scan(&id, &owner)
	if errors.Is(err, sql.ErrNoRows) || err == nil && ownerID != "" && ownerID != owner {
		return project.ErrNotFound
	}
	if err != nil {
		return err
	}
	var current string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM deployments WHERE project_id=$1 AND status<>'draft' ORDER BY created_at DESC,id DESC LIMIT 1", id).Scan(&current); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current != expectedID {
		return project.ErrVersionConflict
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO deployments (id,project_id,status,file_name,created_at,finished_at,error,notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (id) DO UPDATE
		SET status=EXCLUDED.status,created_at=EXCLUDED.created_at,finished_at=EXCLUDED.finished_at
		WHERE deployments.project_id=EXCLUDED.project_id`, next.ID, id, next.Status, next.FileName, next.CreatedAt, next.FinishedAt, next.Error, next.Notes)
	if err != nil {
		return fmt.Errorf("insert revised deployment: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return project.ErrVersionConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE projects SET updated_at=$1,status=CASE WHEN status='draft' THEN 'online' ELSE status END WHERE id=$2", next.CreatedAt, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM deployments WHERE project_id=$1 AND status<>'draft' AND id NOT IN ($2,$3)", id, next.ID, current); err != nil {
		return err
	}
	return tx.Commit()
}
