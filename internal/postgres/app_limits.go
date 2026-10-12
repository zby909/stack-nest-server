package postgres

import (
	"context"
	"database/sql"
	"stacknest/server/internal/containerapp"
)

const limitsColumns = `max_apps,total_memory_mib,revision,(SELECT count(*) FROM container_apps),(SELECT COALESCE(sum(memory_mib),0) FROM container_apps)`

// scanAppLimits 使用同一数据库快照读取预算和所有应用的预留资源。
func scanAppLimits(row interface{ Scan(...any) error }) (containerapp.LimitsView, error) {
	var view containerapp.LimitsView
	err := row.Scan(&view.MaxApps, &view.TotalMemoryMiB, &view.Revision, &view.UsedApps, &view.AllocatedMemoryMiB)
	return view, err
}

// AppLimits 返回实例唯一的已保存配置，不依赖后端内存缓存。
func (s *Store) AppLimits(ctx context.Context) (containerapp.LimitsView, error) {
	return scanAppLimits(s.db.QueryRowContext(ctx, "SELECT "+limitsColumns+" FROM app_limits WHERE id=1"))
}

// UpdateAppLimits 与创建应用共用事务锁，避免保存配置和部署并发超额。
func (s *Store) UpdateAppLimits(ctx context.Context, input containerapp.LimitsUpdate) (containerapp.LimitsView, error) {
	if err := input.Limits.Validate(); err != nil {
		return containerapp.LimitsView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return containerapp.LimitsView{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(714581130)"); err != nil {
		return containerapp.LimitsView{}, err
	}
	view, err := scanAppLimits(tx.QueryRowContext(ctx, "SELECT "+limitsColumns+" FROM app_limits WHERE id=1 FOR UPDATE"))
	if err != nil {
		return view, err
	}
	if input.ExpectedRevision != view.Revision {
		return view, containerapp.ErrConflict
	}
	var busy bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM container_apps WHERE status IN ('pending','deploying'))").Scan(&busy); err != nil {
		return view, err
	}
	if busy {
		return view, containerapp.ErrBusy
	}
	if input.MaxApps < view.UsedApps || input.TotalMemoryMiB < view.AllocatedMemoryMiB {
		return view, containerapp.ErrQuota
	}
	if _, err = tx.ExecContext(ctx, "UPDATE app_limits SET max_apps=$1,total_memory_mib=$2,revision=revision+1 WHERE id=1", input.MaxApps, input.TotalMemoryMiB); err != nil {
		return view, err
	}
	view.Limits = input.Limits
	view.Revision++
	return view, tx.Commit()
}

// transactionLimits 在持有实例锁的应用事务中读取实际预算。
func transactionLimits(ctx context.Context, tx *sql.Tx) (containerapp.Limits, error) {
	var value containerapp.Limits
	err := tx.QueryRowContext(ctx, "SELECT max_apps,total_memory_mib FROM app_limits WHERE id=1").Scan(&value.MaxApps, &value.TotalMemoryMiB)
	return value, err
}
