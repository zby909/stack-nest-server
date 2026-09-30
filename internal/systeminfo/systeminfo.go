package systeminfo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("system information unavailable")

// Usage 是从 Linux 主机读取的 CPU、内存与运行时长快照。
type Usage struct {
	CPUPercent           float64
	CPUCores             int
	MemoryTotalBytes     uint64
	MemoryAvailableBytes uint64
	MemoryUsedPercent    float64
	UptimeSeconds        uint64
}

type cpuSample struct {
	total uint64
	idle  uint64
}

// Read 在短采样窗口内读取 CPU 负载，并读取主机内存与运行时间。
func Read(ctx context.Context) (Usage, error) {
	if runtime.GOOS != "linux" {
		return Usage{}, fmt.Errorf("%w: unsupported operating system", ErrUnavailable)
	}
	first, err := readCPU()
	if err != nil {
		return Usage{}, err
	}
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Usage{}, ctx.Err()
	case <-timer.C:
	}
	second, err := readCPU()
	if err != nil {
		return Usage{}, err
	}
	if second.total <= first.total || second.idle < first.idle {
		return Usage{}, ErrUnavailable
	}
	deltaTotal := second.total - first.total
	deltaIdle := second.idle - first.idle
	if deltaIdle > deltaTotal {
		return Usage{}, ErrUnavailable
	}
	memory, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Usage{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	total, available, err := parseMemory(string(memory))
	if err != nil {
		return Usage{}, err
	}
	uptime, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return Usage{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	uptimeFields := strings.Fields(string(uptime))
	if len(uptimeFields) == 0 {
		return Usage{}, ErrUnavailable
	}
	seconds, err := strconv.ParseFloat(uptimeFields[0], 64)
	if err != nil || seconds < 0 {
		return Usage{}, ErrUnavailable
	}
	return Usage{
		CPUPercent:           float64(deltaTotal-deltaIdle) * 100 / float64(deltaTotal),
		CPUCores:             runtime.NumCPU(),
		MemoryTotalBytes:     total,
		MemoryAvailableBytes: available,
		MemoryUsedPercent:    float64(total-available) * 100 / float64(total),
		UptimeSeconds:        uint64(seconds),
	}, nil
}

// readCPU 读取 Linux 累计 CPU 计数器。
func readCPU() (cpuSample, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSample{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return parseCPU(string(data))
}

// parseCPU 只解析所有核心汇总行，避免暴露进程和核心细节。
func parseCPU(data string) (cpuSample, error) {
	fields := strings.Fields(strings.SplitN(data, "\n", 2)[0])
	if len(fields) < 6 || fields[0] != "cpu" {
		return cpuSample{}, ErrUnavailable
	}
	var sample cpuSample
	for index, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuSample{}, ErrUnavailable
		}
		sample.total += value
		if index == 3 || index == 4 {
			sample.idle += value
		}
	}
	return sample, nil
}

// parseMemory 读取 MemAvailable，避免将可回收缓存误算为内存占用。
func parseMemory(data string) (uint64, uint64, error) {
	var total, available uint64
	var foundAvailable bool
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, 0, ErrUnavailable
		}
		switch fields[0] {
		case "MemTotal:":
			total = value * 1024
		case "MemAvailable:":
			available = value * 1024
			foundAvailable = true
		}
	}
	if total == 0 || !foundAvailable || available > total {
		return 0, 0, ErrUnavailable
	}
	return total, available, nil
}
