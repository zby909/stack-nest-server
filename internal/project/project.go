package project

import (
	"context"
	"errors"
	"io"
	"os"
	"time"
)

var (
	ErrNotFound           = errors.New("project not found")
	ErrAlreadyExists      = errors.New("project already exists")
	ErrInvalidName        = errors.New("project name is required")
	ErrInvalidDescription = errors.New("project description is too long")
	ErrInvalidSlug        = errors.New("project slug is invalid")
	ErrInvalidArchive     = errors.New("project archive is invalid")
	ErrMissingOwner       = errors.New("project owner is required")
	ErrVersionConflict    = errors.New("project deployment changed")
	ErrNoPrevious         = errors.New("project has no previous deployment")
	ErrReleaseBusy        = errors.New("project release needs recovery")
	ErrExportUnavailable  = errors.New("project files cannot be exported safely")
	ErrUsageUnavailable   = errors.New("project files cannot be measured safely")
	ErrDraftExists        = errors.New("project already has a draft")
	ErrInvalidNotes       = errors.New("release notes are too long")
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
	Draft       *Deployment  `json:"draft,omitempty"`
}

// Deployment 表示一次静态项目部署记录。
type Deployment struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	FileName   string    `json:"fileName"`
	CreatedAt  time.Time `json:"createdAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Error      string    `json:"error,omitempty"`
	Notes      string    `json:"notes,omitempty"`
}

// CreateInput 是创建静态项目所需的数据。
type CreateInput struct {
	Preview      bool
	Notes        string
	DeploymentID string // 仅由服务端续传会话生成，用于确认重复发布。
	OwnerID      string
	OwnerEmail   string
	Name         string
	Slug         string
	Description  string
	FileName     string
	Source       io.Reader
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
	SaveDraft(ctx context.Context, slug, ownerID, expectedID, replaceDraftID string, deployment Deployment) error
	DeleteDraft(ctx context.Context, slug, ownerID, draftID string) error
	UpdateMetadata(ctx context.Context, value Project, expectedAt time.Time) error
	Delete(ctx context.Context, slug, ownerID string) error
}

// Actor 是执行项目操作的已认证用户。
type Actor struct {
	ID        string
	IsAdmin   bool
	ProjectID string // 部署凭据绑定不可变项目 ID，阻止同名项目删除重建后复用。
}

// Publisher 定义静态项目文件的发布能力。
type Publisher interface {
	Changes(ctx context.Context, slug, base, target string) ([]FileChange, error)
	OpenVersion(slug, slot string) (*os.Root, error)
	PrepareDraft(ctx context.Context, slug, id, fileName string, source io.Reader) (PublishedFiles, error)
	RemoveDraft(slug, id string) error
	FilesAt(ctx context.Context, slug, slot string) ([]PublishedFile, error)
	MeasureAt(ctx context.Context, slug, slot string) (PublishedFiles, error)
	ExportAt(ctx context.Context, slug, slot string) (Archive, error)
	Files(ctx context.Context, slug string, previous bool) ([]PublishedFile, error)
	Measure(ctx context.Context, slug string, previous bool) (PublishedFiles, error)
	SetPaused(slug string, paused bool) error
	Publish(ctx context.Context, slug, fileName string, source io.Reader) (PublishedFiles, error)
	CompletePublication(slug string) error
	Delete(ctx context.Context, slug string) error
	StageDelete(ctx context.Context, slug string) (Deletion, error)
	StageRevision(ctx context.Context, input RevisionInput) (Revision, error)
	Export(ctx context.Context, slug string, previous bool) (Archive, error)
}

// MetadataInput 仅允许修改展示信息，并校验页面读取时的更新时间。
type MetadataInput struct {
	Name        string
	Description string
	ExpectedAt  time.Time
}

// Archive 是可定位读取的 ZIP 快照，关闭后立即清理临时文件。
type Archive interface {
	io.ReadSeekCloser
	Size() int64
}

// ExportedProject 描述已经准备好的部署快照，不依赖客户端的下载速度。
type ExportedProject struct {
	Archive    Archive
	Deployment Deployment
	OwnerID    string
}

// UpdateInput 只替换文件，保留项目身份、归属、地址和说明。
type UpdateInput struct {
	ReplaceDraftID string // 非空时仅替换这个待发布版，不能跟随最新草稿。
	Preview        bool
	Notes          string
	DeploymentID   string // 同一续传记录始终使用相同部署编号。
	ExpectedID     string
	FileName       string
	Source         io.Reader
}

// RevisionInput 将文件事务与数据库中的前后部署编号对应起来。
type RevisionInput struct {
	DraftID   string
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
