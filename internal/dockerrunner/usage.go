package dockerrunner

import (
	"context"
	"errors"
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

// usage 优先采集 CPU 增量，超时后尝试单次采集内存和进程数。
func (r *Runner) usage(ctx context.Context, cid string) (*containerapp.Usage, error) {
	path := "/containers/" + url.PathEscape(cid) + "/stats?stream=false"
	// 为两个 Docker 采集周期留出余量，同时给健康检查保留时间。
	task, cancel := context.WithTimeout(ctx, 2250*time.Millisecond)
	var stats resourceStats
	err := r.engine.call(task, "GET", path, nil, &stats)
	timedOut := task.Err() == context.DeadlineExceeded
	cancel()
	if err != nil && timedOut && ctx.Err() == nil {
		fallback, done := context.WithTimeout(ctx, 500*time.Millisecond)
		defer done()
		stats = resourceStats{}
		err = r.engine.call(fallback, "GET", path+"&one-shot=true", nil, &stats)
	}
	if err != nil {
		return nil, err
	}
	if stats.Read.IsZero() {
		return nil, errors.New("Docker 未返回有效的资源采样")
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
