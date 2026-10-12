package containerapp

import "context"

// Limits 是实例共享的应用预算；单个应用的限制仍由 Spec 校验。
type Limits struct {
	MaxApps        int `json:"maxApps"`
	TotalMemoryMiB int `json:"totalMemoryMiB"`
}

// LimitsView 同时返回保存版本和已预留资源，停止的应用也占用预算。
type LimitsView struct {
	Limits
	Revision           int64 `json:"revision"`
	UsedApps           int   `json:"usedApps"`
	AllocatedMemoryMiB int   `json:"allocatedMemoryMiB"`
	MaxTotalMemoryMiB  int   `json:"maxTotalMemoryMiB"`
}

// LimitsUpdate 拒绝旧页面覆盖更新后的实例预算。
type LimitsUpdate struct {
	Limits
	ExpectedRevision int64 `json:"expectedRevision"`
}

// Capacity 只通过内部运行器接口提供可配置预算的安全上限。
type Capacity struct {
	MaxTotalMemoryMiB int `json:"maxTotalMemoryMiB"`
}

// DefaultLimits 保留升级前的实例预算。
func DefaultLimits() Limits { return Limits{MaxApps: MaxApps, TotalMemoryMiB: TotalMemoryMiB} }

// Validate 检查有限的实例配置范围，不允许关闭资源限制。
func (l Limits) Validate() error {
	if l.MaxApps < 1 || l.MaxApps > 100 || l.TotalMemoryMiB < 64 || l.TotalMemoryMiB > 131072 {
		return invalid("应用总数须为 1–100，内存总预算须为 64–131072 MiB")
	}
	return nil
}

// Limits 返回数据库内唯一的实例预算。
func (s *Service) Limits(ctx context.Context) (LimitsView, error) { return s.store.AppLimits(ctx) }

// LimitSettings 为超管补充运行器核对的预算上限。
func (s *Service) LimitSettings(ctx context.Context) (LimitsView, error) {
	view, err := s.Limits(ctx)
	if err != nil {
		return view, err
	}
	capacity, err := s.runtime.Capacity(ctx)
	view.MaxTotalMemoryMiB = capacity.MaxTotalMemoryMiB
	return view, err
}

// UpdateLimits 在数据库事务中核对版本、已有预留资源和宿主容量。
func (s *Service) UpdateLimits(ctx context.Context, input LimitsUpdate) (LimitsView, error) {
	if err := input.Limits.Validate(); err != nil {
		return LimitsView{}, err
	}
	capacity, err := s.runtime.Capacity(ctx)
	if err != nil {
		return LimitsView{}, err
	}
	if input.TotalMemoryMiB > capacity.MaxTotalMemoryMiB {
		return LimitsView{}, invalid("内存总预算超过当前服务器允许的上限，请为系统和平台保留空间")
	}
	view, err := s.store.UpdateAppLimits(ctx, input)
	view.MaxTotalMemoryMiB = capacity.MaxTotalMemoryMiB
	return view, err
}
