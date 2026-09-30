package systeminfo

import (
	"context"
	"runtime"
	"testing"
)

// TestParseCPU 验证汇总 CPU 行的空闲与总计数器。
func TestParseCPU(t *testing.T) {
	value, err := parseCPU("cpu  10 2 3 20 5 1 0 0 0 0\ncpu0 1 2 3 4 5\n")
	if err != nil || value.total != 41 || value.idle != 25 {
		t.Fatalf("unexpected CPU sample: %+v %v", value, err)
	}
	if _, err := parseCPU("cpu  malformed"); err == nil {
		t.Fatal("malformed CPU counters accepted")
	}
}

// TestParseMemory 验证可用内存来自 MemAvailable，而不是 MemFree。
func TestParseMemory(t *testing.T) {
	total, available, err := parseMemory("MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 400 kB\n")
	if err != nil || total != 1000*1024 || available != 400*1024 {
		t.Fatalf("unexpected memory: %d %d %v", total, available, err)
	}
	if _, _, err := parseMemory("MemTotal: 1000 kB\n"); err == nil {
		t.Fatal("missing MemAvailable should be treated as unavailable")
	}
}

// TestReadLinux 验证生产 Linux 环境能读取合理的主机资源值。
func TestReadLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux host metrics are unavailable on this platform")
	}
	value, err := Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if value.CPUPercent < 0 || value.CPUPercent > 100 || value.CPUCores < 1 ||
		value.MemoryTotalBytes == 0 || value.MemoryAvailableBytes > value.MemoryTotalBytes ||
		value.MemoryUsedPercent < 0 || value.MemoryUsedPercent > 100 || value.UptimeSeconds == 0 {
		t.Fatalf("invalid Linux metrics: %+v", value)
	}
}
