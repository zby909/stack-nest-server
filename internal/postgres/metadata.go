package postgres

import (
	"context"
	"fmt"
	"time"

	"stacknest/server/internal/project"
)

// UpdateMetadata 原子保存展示信息和访问状态，并校验归属与更新时间。
func (s *Store) UpdateMetadata(ctx context.Context, value project.Project, expectedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET name=$1, description=$2, updated_at=$3, status=$8
		WHERE id=$4 AND slug=$5 AND owner_user_id=$6 AND updated_at=$7`,
		value.Name, value.Description, value.UpdatedAt, value.ID, value.Slug, value.OwnerID, expectedAt, value.Status)
	if err != nil {
		return fmt.Errorf("update project metadata: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read updated project count: %w", err)
	}
	if count == 0 {
		return project.ErrVersionConflict
	}
	return nil
}
