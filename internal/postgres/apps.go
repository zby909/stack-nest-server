package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"stacknest/server/internal/containerapp"
)

const appColumns = `a.id,a.owner_user_id,u.email,a.name,a.slug,a.sealed,a.active_sealed,a.image_id,a.container_id,a.generation,a.action,a.status,a.error,a.actor_id,a.actor_email,a.created_at,a.updated_at,a.memory_mib`

// scanApp 从固定列顺序读取持久化应用。
func scanApp(row interface{ Scan(...any) error }) (containerapp.App, error) {
	var a containerapp.App
	err := row.Scan(&a.ID, &a.OwnerID, &a.OwnerEmail, &a.Name, &a.Slug, &a.Sealed, &a.ActiveSealed, &a.ImageID, &a.ContainerID, &a.Generation, &a.Action, &a.Status, &a.Error, &a.ActorID, &a.ActorEmail, &a.CreatedAt, &a.UpdatedAt, &a.MemoryMiB)
	if errors.Is(err, sql.ErrNoRows) {
		return a, containerapp.ErrNotFound
	}
	return a, err
}

// Apps 在数据库内限制普通用户的可见范围。
func (s *Store) Apps(ctx context.Context, actor containerapp.Actor) ([]containerapp.App, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+appColumns+" FROM container_apps a JOIN auth_users u ON u.id=a.owner_user_id WHERE ($1 OR a.owner_user_id=$2) ORDER BY a.created_at DESC", actor.Admin, actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []containerapp.App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// App 以相同的不存在响应隐藏他人的应用。
func (s *Store) App(ctx context.Context, id string, actor containerapp.Actor) (containerapp.App, error) {
	return scanApp(s.db.QueryRowContext(ctx, "SELECT "+appColumns+" FROM container_apps a JOIN auth_users u ON u.id=a.owner_user_id WHERE a.id=$1 AND ($2 OR a.owner_user_id=$3)", id, actor.Admin, actor.ID))
}

// SaveApp 在同一事务内验证用户、乐观版本和全局配额。
func (s *Store) SaveApp(ctx context.Context, a containerapp.App, actor containerapp.Actor, create bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(714581130)"); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT true FROM auth_users WHERE id=$1 FOR KEY SHARE", actor.ID).Scan(&exists); err != nil {
		return containerapp.ErrNotFound
	}
	oldMemory := 0
	if !create {
		var generation int64
		var status string
		if err = tx.QueryRowContext(ctx, "SELECT generation,status,memory_mib FROM container_apps WHERE id=$1 AND ($2 OR owner_user_id=$3) FOR UPDATE", a.ID, actor.Admin, actor.ID).Scan(&generation, &status, &oldMemory); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return containerapp.ErrNotFound
			}
			return err
		}
		if status == "pending" || status == "deploying" || generation != a.Generation {
			return containerapp.ErrBusy
		}
	}
	// 更新期间保留旧版与候选版中较大的额度，失败恢复不会占用别人的预算。
	if !create && a.MemoryMiB < oldMemory {
		a.MemoryMiB = oldMemory
	}
	limits, err := transactionLimits(ctx, tx)
	if err != nil {
		return err
	}
	var count, memory int
	if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(memory_mib),0) FROM container_apps").Scan(&count, &memory); err != nil {
		return err
	}
	if create && count >= limits.MaxApps || memory-oldMemory+a.MemoryMiB > limits.TotalMemoryMiB {
		return containerapp.ErrQuota
	}
	if create {
		_, err = tx.ExecContext(ctx, `INSERT INTO container_apps(id,owner_user_id,name,slug,sealed,memory_mib,actor_id,actor_email) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, a.ID, actor.ID, a.Name, a.Slug, a.Sealed, a.MemoryMiB, actor.ID, actor.Email)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE container_apps SET name=$2,sealed=$3,memory_mib=$4,generation=generation+1,action='deploy',status='pending',error='',actor_id=$5,actor_email=$6,updated_at=now() WHERE id=$1`, a.ID, a.Name, a.Sealed, a.MemoryMiB, actor.ID, actor.Email)
	}
	var conflict *pgconn.PgError
	if errors.As(err, &conflict) && conflict.Code == "23505" {
		return containerapp.ErrBusy
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// QueueApp 在身份与版本未变化时提交生命周期操作。
func (s *Store) QueueApp(ctx context.Context, id string, actor containerapp.Actor, action string, generation int64) (containerapp.App, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE container_apps SET generation=generation+1,action=$4,status='pending',error='',actor_id=$5,actor_email=$6,updated_at=now() WHERE id=$1 AND ($2 OR owner_user_id=$3) AND generation=$7 AND status NOT IN ('pending','deploying')`, id, actor.Admin, actor.ID, action, actor.ID, actor.Email, generation)
	if err != nil {
		return containerapp.App{}, err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		if _, err = s.App(ctx, id, actor); err != nil {
			return containerapp.App{}, err
		}
		return containerapp.App{}, containerapp.ErrBusy
	}
	return s.App(ctx, id, actor)
}

// RecoverApps 将进程中断的持久化任务重新排队；运行器按编号与代次幂等恢复。
func (s *Store) RecoverApps(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "UPDATE container_apps SET status='pending' WHERE status='deploying'")
	return err
}

// CancelPendingApp 只结束仍在排队的同一部署；已领取或已完成的任务不被覆盖。
func (s *Store) CancelPendingApp(ctx context.Context, a containerapp.App) error {
	_, err := s.db.ExecContext(ctx, `UPDATE container_apps SET status='error',action='',error=$3,sealed=$4,memory_mib=$5,updated_at=now() WHERE id=$1 AND generation=$2 AND status='pending' AND action='deploy'`, a.ID, a.Generation, containerapp.ErrDeployCanceled.Error(), a.Sealed, a.MemoryMiB)
	return err
}

// ClaimApp 原子领取一个任务，不将长时间拉镜像放在数据库事务里。
func (s *Store) ClaimApp(ctx context.Context) (containerapp.App, error) {
	return scanApp(s.db.QueryRowContext(ctx, `WITH picked AS (SELECT id FROM container_apps WHERE status='pending' ORDER BY updated_at FOR UPDATE SKIP LOCKED LIMIT 1), updated AS (UPDATE container_apps SET status='deploying' WHERE id IN (SELECT id FROM picked) RETURNING *) SELECT `+appColumns+` FROM updated a JOIN auth_users u ON u.id=a.owner_user_id`))
}

// CompleteApp 只完成领取的代次，删除失败时保留配置与数据供重试。
func (s *Store) CompleteApp(ctx context.Context, a containerapp.App, result containerapp.Result, message string) error {
	if a.Action == "delete" && message == "" {
		_, err := s.db.ExecContext(ctx, "DELETE FROM container_apps WHERE id=$1 AND generation=$2", a.ID, a.Generation)
		return err
	}
	status := "ready"
	if result.State != "running" {
		status = "stopped"
	}
	if message != "" {
		status = "error"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE container_apps SET status=$3,action='',error=$4,sealed=$5,active_sealed=$6,container_id=$7,image_id=$8,memory_mib=$9,updated_at=now() WHERE id=$1 AND generation=$2`, a.ID, a.Generation, status, message, a.Sealed, a.ActiveSealed, result.ContainerID, result.ImageID, a.MemoryMiB)
	return err
}
