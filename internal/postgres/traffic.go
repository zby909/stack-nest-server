package postgres

import (
	"context"
	"stacknest/server/internal/traffic"
)

// TrafficTargets 只读取正式域名、资源主键和创建时间，统计过程无需访问成员信息。
func (s *Store) TrafficTargets(ctx context.Context) (traffic.Targets, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT 'stack-nest-' || slug,'site:' || id,created_at FROM projects
		UNION ALL SELECT 'stack-nest--app-' || slug,'app:' || id,created_at FROM container_apps`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := traffic.Targets{}
	for rows.Next() {
		var host string
		var target traffic.Target
		if err = rows.Scan(&host, &target.ID, &target.CreatedAt); err != nil {
			return nil, err
		}
		out[host] = target
	}
	return out, rows.Err()
}
