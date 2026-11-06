package containerapp

import (
	"context"
	"time"
)

// Deployment 保存最近部署的摘要，凭据与环境变量不进入历史记录。
type Deployment struct {
	Generation int64      `json:"generation"`
	Image      string     `json:"image"`
	ImageID    string     `json:"imageId,omitempty"`
	ActorEmail string     `json:"actorEmail"`
	Source     string     `json:"source"`
	Status     string     `json:"status"`
	Message    string     `json:"message,omitempty"`
	QueuedAt   time.Time  `json:"queuedAt"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// History 在读取历史前按当前归属验证权限。
func (s *Service) History(ctx context.Context, id string, actor Actor) ([]Deployment, error) {
	if _, err := s.Get(ctx, id, actor); err != nil {
		return nil, err
	}
	return s.store.AppDeployments(ctx, id)
}
