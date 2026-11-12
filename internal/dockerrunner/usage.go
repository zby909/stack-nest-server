package dockerrunner

import (
	"context"
	"errors"
	"net/url"
	"sync"
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
	Read   time.Time `json:"read"`
	CPU    cpuStats  `json:"cpu_stats"`
	Memory struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	PIDs struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

const (
	cpuSampleTTL  = 30 * time.Second
	maxCPUSamples = 128
)

type cpuSample struct {
	read, startedAt time.Time
	stats           cpuStats
}

// cpuHistory 仅保留 CPU 增量所需的上一份读数，内存与进程数始终即时读取。
type cpuHistory struct {
	mu      sync.Mutex
	samples map[string]cpuSample
}

// percent 比较同一轮容器运行中的两次读数，忽略重启、过期或乱序的样本。
func (h *cpuHistory) percent(cid string, current cpuSample) *float64 {
	if current.startedAt.IsZero() || current.stats.System == 0 || current.stats.Online == 0 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.samples == nil {
		h.samples = make(map[string]cpuSample)
	}
	previous, exists := h.samples[cid]
	if exists && (!current.read.After(previous.read) || current.startedAt.Before(previous.startedAt)) {
		return nil
	}
	if !exists && len(h.samples) >= maxCPUSamples {
		for id, sample := range h.samples {
			if current.read.Sub(sample.read) > cpuSampleTTL {
				delete(h.samples, id)
			}
		}
		if len(h.samples) >= maxCPUSamples {
			return nil
		}
	}
	h.samples[cid] = current
	if !exists || !current.startedAt.Equal(previous.startedAt) || current.read.Sub(previous.read) > cpuSampleTTL ||
		current.stats.Online != previous.stats.Online || current.stats.System <= previous.stats.System || current.stats.Usage.Total < previous.stats.Usage.Total {
		return nil
	}
	percent := float64(current.stats.Usage.Total-previous.stats.Usage.Total) / float64(current.stats.System-previous.stats.System) * float64(current.stats.Online) * 100
	return &percent
}

// usage 立即读取单次资源样本，用相邻请求间的 CPU 增量计算占用率。
func (r *Runner) usage(ctx context.Context, cid string, startedAt time.Time) (*containerapp.Usage, error) {
	task, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	path := "/containers/" + url.PathEscape(cid) + "/stats?stream=false&one-shot=true"
	var stats resourceStats
	if err := r.engine.call(task, "GET", path, nil, &stats); err != nil {
		return nil, err
	}
	if stats.Read.IsZero() {
		return nil, errors.New("Docker 未返回有效的资源采样")
	}
	result := stats.view()
	result.CPUPercent = r.cpuHistory.percent(cid, cpuSample{read: stats.Read, startedAt: startedAt, stats: stats.CPU})
	return result, nil
}

// view 使用 Docker CLI 的 Linux 内存口径，扣除非活跃文件缓存。
func (s resourceStats) view() *containerapp.Usage {
	cache := s.Memory.Stats["total_inactive_file"]
	if value, ok := s.Memory.Stats["inactive_file"]; ok {
		cache = value
	}
	memory := s.Memory.Usage
	if cache < memory {
		memory -= cache
	}
	return &containerapp.Usage{MemoryBytes: memory, MemoryLimitBytes: s.Memory.Limit, PIDs: s.PIDs.Current, SampledAt: s.Read}
}
