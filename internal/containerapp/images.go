package containerapp

import (
	"context"
	"errors"
	"regexp"
	"time"
)

var imageIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var ErrImageProtected = errors.New("镜像仍被引用或不在可清理范围，请刷新清单")

// ValidImageID 只接受完整内容编号，禁止用可变标签删除镜像。
func ValidImageID(id string) bool { return imageIDPattern.MatchString(id) }

// ImageUsage 展示逻辑体积与清理资格，共享镜像层不重复承诺可释放空间。
type ImageUsage struct {
	ID        string   `json:"id"`
	Tags      []string `json:"tags"`
	SizeBytes int64    `json:"sizeBytes"`
	Managed   bool     `json:"managed"`
	Removable bool     `json:"removable"`
	Reason    string   `json:"reason"`
}

// ImageInventory 包含宿主镜像层的合计大小及逐项清理检查结果。
type ImageInventory struct {
	TotalBytes int64        `json:"totalBytes"`
	Images     []ImageUsage `json:"images"`
}

// ImageRequest 由 API 根据数据库生成引用保护，浏览器只能选择确切镜像编号。
type ImageRequest struct {
	ID        string   `json:"id,omitempty"`
	Protected []string `json:"protected"`
}

// ImageRuntime 将镜像管理限制为清单和单个非强制删除。
type ImageRuntime interface {
	Images(context.Context, ImageRequest) (ImageInventory, error)
	DeleteImage(context.Context, ImageRequest) error
}

// imageReferences 同时保护当前、待部署和失败恢复配置，以及停止应用的固定镜像。
func (s *Service) imageReferences(ctx context.Context, deleting bool) ([]string, error) {
	apps, err := s.store.Apps(ctx, Actor{Admin: true})
	if err != nil {
		return nil, err
	}
	refs := []string{}
	for _, app := range apps {
		if deleting && (app.Status == "pending" || app.Status == "deploying") {
			return nil, ErrBusy
		}
		if app.ImageID != "" {
			refs = append(refs, app.ImageID)
		}
		for _, sealed := range []string{app.Sealed, app.ActiveSealed} {
			if sealed == "" {
				continue
			}
			spec, err := open(s.secret, app.ID, sealed)
			if err != nil {
				return nil, err
			}
			refs = append(refs, spec.Image)
		}
	}
	return refs, nil
}

// Images 读取超管清单，引用读取或运行器失败时不提供可删除结果。
func (s *Service) Images(ctx context.Context) (ImageInventory, error) {
	runtime, ok := s.runtime.(ImageRuntime)
	if !ok {
		return ImageInventory{}, ErrUnavailable
	}
	refs, err := s.imageReferences(ctx, false)
	if err != nil {
		return ImageInventory{}, err
	}
	return runtime.Images(ctx, ImageRequest{Protected: refs})
}

// DeleteImage 与新任务入队互斥，删除前重新读取所有应用的保护引用。
func (s *Service) DeleteImage(ctx context.Context, id string) error {
	if !ValidImageID(id) {
		return invalid("镜像编号无效")
	}
	if !s.imageMu.TryLock() {
		return ErrBusy
	}
	defer s.imageMu.Unlock()
	runtime, ok := s.runtime.(ImageRuntime)
	if !ok {
		return ErrUnavailable
	}
	refs, err := s.imageReferences(ctx, true)
	if err != nil {
		return err
	}
	return runtime.DeleteImage(ctx, ImageRequest{ID: id, Protected: refs})
}

// Images 按需读取镜像空间，避免持续轮询 Docker 磁盘统计。
func (c *Client) Images(ctx context.Context, input ImageRequest) (ImageInventory, error) {
	task, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var value ImageInventory
	err := c.call(task, "POST", "/images/list", input, &value)
	return value, imageError(err)
}

// DeleteImage 使用非强制单镜像操作，运行器重新验证归属与全部容器引用。
func (c *Client) DeleteImage(ctx context.Context, input ImageRequest) error {
	task, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	return imageError(c.call(task, "POST", "/images/delete", input, nil))
}

// imageError 仅将已知冲突跨越运行器边界，其他失败统一返回不可用。
func imageError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrBusy, ErrImageProtected, ErrNotFound} {
		if err.Error() == known.Error() {
			return known
		}
	}
	return ErrUnavailable
}
