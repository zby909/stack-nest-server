package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"

	entdb "stacknest/server/internal/ent"
	entdeployment "stacknest/server/internal/ent/deployment"
	entproject "stacknest/server/internal/ent/project"
	"stacknest/server/internal/project"
)

// Store 使用 PostgreSQL + Ent 实现项目持久化。
type Store struct {
	client *entdb.Client
	db     *sql.DB
}

// Open 建立 PostgreSQL 连接并创建 Ent Client。
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	driver := entsql.OpenDB(dialect.Postgres, db)
	return &Store{
		client: entdb.NewClient(entdb.Driver(driver)),
		db:     db,
	}, nil
}

// Close 关闭 Ent Client 和底层数据库连接池。
func (s *Store) Close() error {
	return s.client.Close()
}

// Ping 使用请求的截止时间检查数据库连接，不扫描业务数据或修改记录。
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// List 返回全部项目，按更新时间倒序，并加载部署记录。
func (s *Store) List(ctx context.Context) ([]project.Project, error) {
	rows, err := s.client.Project.
		Query().
		Order(entdb.Desc(entproject.FieldUpdatedAt)).
		WithDeployments(func(query *entdb.DeploymentQuery) {
			query.Order(entdb.Desc(entdeployment.FieldCreatedAt))
		}).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}

	values := make([]project.Project, 0, len(rows))
	emails, err := s.ownerEmails(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		value := toProject(row)
		value.OwnerEmail = emails[value.OwnerID]
		values = append(values, value)
	}
	return values, nil
}

// Get 按 slug 查询项目并加载部署记录。
func (s *Store) Get(ctx context.Context, slug string) (project.Project, error) {
	row, err := s.client.Project.
		Query().
		Where(entproject.SlugEQ(slug)).
		WithDeployments(func(query *entdb.DeploymentQuery) {
			query.Order(entdb.Desc(entdeployment.FieldCreatedAt))
		}).
		Only(ctx)
	if entdb.IsNotFound(err) {
		return project.Project{}, project.ErrNotFound
	}
	if err != nil {
		return project.Project{}, fmt.Errorf("query project: %w", err)
	}
	value := toProject(row)
	if err := s.db.QueryRowContext(ctx, "SELECT email FROM auth_users WHERE id=$1", value.OwnerID).Scan(&value.OwnerEmail); err != nil {
		return project.Project{}, fmt.Errorf("query project owner: %w", err)
	}
	return value, nil
}

// Create 使用事务同时写入项目和部署记录。
func (s *Store) Create(ctx context.Context, value project.Project) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin create project transaction: %w", err)
	}

	if _, err := tx.Project.
		Create().
		SetID(value.ID).
		SetOwnerUserID(value.OwnerID).
		SetName(value.Name).
		SetSlug(value.Slug).
		SetDescription(value.Description).
		SetStatus(value.Status).
		SetCreatedAt(value.CreatedAt).
		SetUpdatedAt(value.UpdatedAt).
		Save(ctx); err != nil {
		_ = tx.Rollback()
		if uniqueViolation(err) {
			return project.ErrAlreadyExists
		}
		return fmt.Errorf("create project: %w", err)
	}

	for _, deployment := range value.Deployments {
		var finishedAt *time.Time
		if !deployment.FinishedAt.IsZero() {
			finishedAt = &deployment.FinishedAt
		}

		if _, err := tx.Deployment.
			Create().
			SetID(deployment.ID).
			SetProjectID(value.ID).
			SetStatus(deployment.Status).
			SetFileName(deployment.FileName).
			SetCreatedAt(deployment.CreatedAt).
			SetNillableFinishedAt(finishedAt).
			SetError(deployment.Error).
			Save(ctx); err != nil {
			return rollback(tx, fmt.Errorf("create deployment: %w", err))
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create project transaction: %w", err)
	}
	return nil
}

// Delete 删除项目；部署记录由数据库外键 ON DELETE CASCADE 清理。
func (s *Store) Delete(ctx context.Context, slug, ownerID string) error {
	query := s.client.Project.Delete().Where(entproject.SlugEQ(slug))
	if ownerID != "" {
		query = query.Where(entproject.OwnerUserIDEQ(ownerID))
	}
	affected, err := query.Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if affected == 0 {
		return project.ErrNotFound
	}
	return nil
}

func toProject(row *entdb.Project) project.Project {
	deployments := make([]project.Deployment, 0, len(row.Edges.Deployments))
	for _, item := range row.Edges.Deployments {
		finishedAt := time.Time{}
		if item.FinishedAt != nil {
			finishedAt = *item.FinishedAt
		}
		deployments = append(deployments, project.Deployment{
			ID:         item.ID,
			Status:     item.Status,
			FileName:   item.FileName,
			CreatedAt:  item.CreatedAt,
			FinishedAt: finishedAt,
			Error:      item.Error,
		})
	}

	return project.Project{
		ID:          row.ID,
		OwnerID:     row.OwnerUserID,
		Name:        row.Name,
		Slug:        row.Slug,
		Description: row.Description,
		Status:      row.Status,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		Deployments: deployments,
	}
}

// ownerEmails 批量读取项目所有者邮箱，避免项目列表产生逐项查询。
func (s *Store) ownerEmails(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,email FROM auth_users")
	if err != nil {
		return nil, fmt.Errorf("query project owner emails: %w", err)
	}
	defer rows.Close()
	emails := make(map[string]string)
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			return nil, fmt.Errorf("scan project owner email: %w", err)
		}
		emails[id] = email
	}
	return emails, rows.Err()
}

func rollback(tx *entdb.Tx, cause error) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("%w; rollback: %v", cause, err)
	}
	return cause
}
