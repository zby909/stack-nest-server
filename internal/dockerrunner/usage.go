package dockerrunner

import (
	"context"
	"net/url"
	"time"

	"stacknest/server/internal/containerapp"
)

// cpuStats 只解析计算容器 CPU 增量所需的计数。
type cpuStats struct {
	Usage struct {
		Total uint64 `json:"total_usage"`
	} `json:"cpu_usage"`
	System uint64 `json:"system_cpu_usage"`
	Online uint64 `json:"online_cpus"`
}

// resourceStats 保留单个容器的计数，不把 Docker 原始统计返回后台。
type resourceStats struct {
	Read    time.Time `json:"read"`
	PreRead time.Time `json:"preread"`
	CPU     cpuStats  `json:"cpu_stats"`
	PreCPU  cpuStats  `json:"precpu_stats"`
	Memory  struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	PIDs struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

// usage 采样两次 CPU 计数并限制请求时间，不创建持续监控流。
func (r *Runner) usage(ctx context.Context, cid string) (*containerapp.Usage, error) {
	task, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var stats resourceStats
	if err := r.engine.call(task, "GET", "/containers/"+url.PathEscape(cid)+"/stats?stream=false", nil, &stats); err != nil {
		return nil, err
	}
	return stats.view(), nil
}

// view 使用 Docker CLI 的 Linux 内存口径，缺少有效 CPU 增量时保留未知值。
func (s resourceStats) view() *containerapp.Usage {
	cache := s.Memory.Stats["total_inactive_file"]
	if value, ok := s.Memory.Stats["inactive_file"]; ok {
		cache = value
	}
	memory := s.Memory.Usage
	if cache < memory {
		memory -= cache
	}
	result := &containerapp.Usage{MemoryBytes: memory, MemoryLimitBytes: s.Memory.Limit, PIDs: s.PIDs.Current, SampledAt: s.Read}
	if !s.PreRead.IsZero() && s.PreCPU.System > 0 && s.CPU.System > s.PreCPU.System && s.CPU.Usage.Total >= s.PreCPU.Usage.Total && s.CPU.Online > 0 {
		percent := float64(s.CPU.Usage.Total-s.PreCPU.Usage.Total) / float64(s.CPU.System-s.PreCPU.System) * float64(s.CPU.Online) * 100
		result.CPUPercent = &percent
	}
	return result
}
