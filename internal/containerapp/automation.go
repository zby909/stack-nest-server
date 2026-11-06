package containerapp

import (
	"context"
	"strings"

	"stacknest/server/internal/deploytoken"
)

// ImageUpdate 只接受镜像版本、明确基线和幂等编号，不允许修改容器配置。
type ImageUpdate struct {
	Image              string `json:"image"`
	ExpectedGeneration int64  `json:"expectedGeneration"`
	RequestID          string `json:"requestId"`
}

// imageRepository 去除版本标记，保留原有仓库地址，避免凭据被发送到另一仓库。
func imageRepository(image string) string {
	if repository, _, ok := strings.Cut(image, "@"); ok {
		return repository
	}
	if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		return image[:colon]
	}
	return image
}

// DeployImage 沿用加密配置，只改变镜像；存储事务再校验凭据、归属与当前代次。
func (s *Service) DeployImage(ctx context.Context, grant deploytoken.Grant, input ImageUpdate) (App, error) {
	s.imageMu.RLock()
	defer s.imageMu.RUnlock()
	if grant.AppID == "" || grant.ProjectID != "" {
		return App{}, deploytoken.ErrInvalid
	}
	if !ValidID(input.RequestID) || input.ExpectedGeneration < 1 {
		return App{}, invalid("请提供规范 requestId 和明确的 expectedGeneration")
	}
	a, err := s.Get(ctx, grant.AppID, Actor{ID: grant.OwnerID})
	if err != nil {
		return a, err
	}
	input.Image = strings.TrimSpace(input.Image)
	if imageRepository(a.Image) != imageRepository(input.Image) {
		return a, invalid("自动部署只允许更新同一镜像仓库的版本；更换仓库请在后台修改")
	}
	if !strings.Contains(input.Image, "@") && strings.LastIndex(input.Image, ":") <= strings.LastIndex(input.Image, "/") {
		return a, invalid("请明确指定镜像标签或 SHA-256 摘要")
	}
	spec, err := open(s.secret, a.ID, a.Sealed)
	if err != nil {
		return a, err
	}
	spec.Image = input.Image
	if err = spec.Validate(); err != nil {
		return a, err
	}
	if err = s.runtime.Health(ctx); err != nil {
		return a, ErrUnavailable
	}
	if err = s.guard.Check(2 * MaxImageBytes); err != nil {
		return a, invalid("磁盘可用空间不足，已暂停部署")
	}
	a.Sealed, err = seal(s.secret, a.ID, spec)
	if err != nil {
		return a, err
	}
	generation, err := s.store.SaveAppImage(ctx, a, grant, input)
	if err != nil {
		return a, err
	}
	a, err = s.Get(ctx, grant.AppID, Actor{ID: grant.OwnerID})
	if err == nil && a.Generation != generation {
		err = ErrConflict
	}
	return a, err
}
