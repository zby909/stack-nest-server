package containerapp

import "time"

// DiskArea 区分未知用量、未配置硬配额和内核实际块计数，零值不冒充采样成功。
type DiskArea struct {
	UsedBytes   *uint64 `json:"usedBytes,omitempty"`
	LimitBytes  *uint64 `json:"limitBytes,omitempty"`
	UsedInodes  *uint64 `json:"usedInodes,omitempty"`
	LimitInodes *uint64 `json:"limitInodes,omitempty"`
	Source      string  `json:"source"`
}

// DiskUsage 仅包含本应用用量；镜像共享层、日志和临时内存盘不计入这两项。
type DiskUsage struct {
	Writable   DiskArea  `json:"writable"`
	Persistent *DiskArea `json:"persistent,omitempty"`
	SampledAt  time.Time `json:"sampledAt"`
}
