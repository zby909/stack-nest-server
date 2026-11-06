package postgres

import (
	"context"
	"database/sql"
	"stacknest/server/internal/containerapp"
)

// queueAppDeployment 与配置入队共同提交，每应用仅保留最近十次部署摘要。
func queueAppDeployment(ctx context.Context, tx *sql.Tx, id, image, source string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO app_deployments(app_id,generation,image,actor_email,source)
		SELECT id,generation,$2,actor_email,$3 FROM container_apps WHERE id=$1`, id, image, source)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM app_deployments WHERE app_id=$1 AND generation NOT IN
		(SELECT generation FROM app_deployments WHERE app_id=$1 ORDER BY generation DESC LIMIT 10)`, id)
	return err
}

// AppDeployments 返回已由服务层验证归属的有界部署记录。
func (s *Store) AppDeployments(ctx context.Context, id string) ([]containerapp.Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT generation,image,image_id,actor_email,source,status,message,queued_at,started_at,finished_at
		FROM app_deployments WHERE app_id=$1 ORDER BY generation DESC LIMIT 10`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []containerapp.Deployment{}
	for rows.Next() {
		var item containerapp.Deployment
		if err := rows.Scan(&item.Generation, &item.Image, &item.ImageID, &item.ActorEmail, &item.Source, &item.Status, &item.Message, &item.QueuedAt, &item.StartedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
