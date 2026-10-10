package project

import "context"

// PublishedFile 只暴露部署内的相对路径与字节数。
type PublishedFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// FilesAs 在文件切换锁内检查权限和版本，避免读取过程中被更新或删除。
func (s *Service) FilesAs(ctx context.Context, slug string, actor Actor, deploymentID string) ([]PublishedFile, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	value, err := s.store.Get(ctx, slug)
	if err != nil {
		return nil, err
	}
	if !actor.IsAdmin && (actor.ID == "" || actor.ID != value.OwnerID) {
		return nil, ErrNotFound
	}
	if value.Draft != nil && value.Draft.ID == deploymentID {
		return s.publisher.FilesAt(ctx, slug, deploymentID)
	}
	for index, deployment := range value.Deployments {
		if index > 1 {
			break
		}
		if deployment.ID == deploymentID && deploymentID != "" {
			return s.publisher.Files(ctx, slug, index == 1)
		}
	}
	return nil, ErrVersionConflict
}
