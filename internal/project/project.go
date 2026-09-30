package project

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound        = errors.New("project not found")
	ErrAlreadyExists   = errors.New("project already exists")
	ErrInvalidName     = errors.New("project name is required")
	ErrInvalidSlug     = errors.New("project slug is invalid")
	ErrInvalidArchive  = errors.New("project archive is invalid")
	ErrMissingOwner    = errors.New("project owner is required")
	ErrVersionConflict = errors.New("project deployment changed")
	ErrNoPrevious      = errors.New("project has no previous deployment")
	ErrReleaseBusy     = errors.New("project release needs recovery")
)

// InvalidArchiveError 表示由用户上传内容导致的压缩包校验失败。
type InvalidArchiveError struct {
	Reason string
}

// Error 返回可安全展示给用户的校验原因。
func (e *InvalidArchiveError) Error() string {
	return e.Reason
}

// Unwrap 允许通过 errors.Is 识别压缩包校验错误。
func (e *InvalidArchiveError) Unwrap() error {
	return ErrInvalidArchive
}

// NewInvalidArchiveError 创建带明确原因的压缩包校验错误。
func NewInvalidArchiveError(reason string) error {
	return &InvalidArchiveError{Reason: reason}
}

// Project 表示一个长期存在的静态项目。
type Project struct {
	ID          string       `json:"id"`
	OwnerID     string       `json:"ownerId"`
	OwnerEmail  string       `json:"ownerEmail"`
	Name        string       `json:"name"`
	Slug        string       `json:"slug"`
	Description string       `json:"description,omitempty"`
	Status      string       `json:"status"`
	CreatedAt   time.Time    `json:"createdAt"`
	UpdatedAt   time.Time    `json:"updatedAt"`
	Deployments []Deployment `json:"deployments,omitempty"`
}

// Deployment 表示一次静态项目部署记录。
type Deployment struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	FileName   string    `json:"fileName"`
	CreatedAt  time.Time `json:"createdAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// CreateInput 是创建静态项目所需的数据。
type CreateInput struct {
	OwnerID     string
	OwnerEmail  string
	Name        string
	Slug        string
	Description string
	FileName    string
	Source      io.Reader
}

// PublishedFiles 记录实际解压文件的大小和数量。
type PublishedFiles struct {
	Bytes int64
	Count int
}

// CreatedProject 携带本次发布的实际统计，供操作日志使用。
type CreatedProject struct {
	Project Project
	Files   PublishedFiles
}

// Store 定义 Project 业务真正需要的持久化能力。
type Store interface {
	List(ctx context.Context) ([]Project, error)
	Get(ctx context.Context, slug string) (Project, error)
	Create(ctx context.Context, project Project) error
	ReplaceDeployment(ctx context.Context, slug, ownerID, expectedID string, deployment Deployment) error
	Delete(ctx context.Context, slug, ownerID string) error
}

// Actor 是执行项目操作的已认证用户。
type Actor struct {
	ID      string
	IsAdmin bool
}

// Publisher 定义静态项目文件的发布能力。
type Publisher interface {
	Publish(ctx context.Context, slug, fileName string, source io.Reader) (PublishedFiles, error)
	CompletePublication(slug string) error
	Delete(ctx context.Context, slug string) error
	StageDelete(ctx context.Context, slug string) (Deletion, error)
	StageRevision(ctx context.Context, input RevisionInput) (Revision, error)
}

// UpdateInput 只替换文件，保留项目身份、归属、地址和说明。
type UpdateInput struct {
	ExpectedID string
	FileName   string
	Source     io.Reader
}

// RevisionInput 将文件事务与数据库中的前后部署编号对应起来。
type RevisionInput struct {
	Slug      string
	ProjectID string
	FromID    string
	ToID      string
	FileName  string
	Source    io.Reader
	Rollback  bool
}

// Revision 在元数据提交前保留所有可恢复文件。
type Revision interface {
	Deletion
	Files() PublishedFiles
}

// Deletion 暂存待删除文件，允许元数据写入失败时恢复站点。
type Deletion interface {
	Commit() error
	Rollback() error
}
