package project

import "context"

// FileChange 表示两个版本之间的文件变化，不返回文件内容。
type FileChange struct {
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	BeforeBytes int64  `json:"beforeBytes"`
	AfterBytes  int64  `json:"afterBytes"`
}

// ChangesAs 在版本锁内比较实际文件内容，同大小修改也会被识别。
func (s *Service) ChangesAs(ctx context.Context, slug string, actor Actor, targetID, baseID string) ([]FileChange, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	value, err := s.GetAs(ctx, slug, actor)
	if err != nil {
		return nil, err
	}
	target, ok := VersionSlot(value, targetID)
	if !ok {
		return nil, ErrVersionConflict
	}
	base := ""
	if baseID != "" {
		base, ok = VersionSlot(value, baseID)
		if !ok {
			return nil, ErrVersionConflict
		}
	}
	return s.publisher.Changes(ctx, slug, base, target)
}
