package storage

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// TestCapacityThreshold 验证阈值边界和预留空间，而非仅比较当前用量。
func TestCapacityThreshold(t *testing.T) {
	for _, test := range []struct {
		name   string
		free   uint64
		extra  int64
		denied bool
	}{
		{"at 90 percent", 100, 0, true}, {"over 90 percent", 99, 0, true},
		{"below threshold", 101, 0, false}, {"projected threshold", 200, 100, true},
		{"sufficient headroom", 201, 100, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := CheckUsage(Usage{TotalBytes: 1000, FreeBytes: test.free}, test.extra)
			if errors.Is(err, ErrInsufficient) != test.denied {
				t.Fatalf("unexpected capacity decision: %v", err)
			}
		})
	}
}

// TestCopyStopsWhenDiskFills 验证其他进程占用空间后会在复制期间停止。
func TestCopyStopsWhenDiskFills(t *testing.T) {
	calls := 0
	guard := &Guard{Dir: t.TempDir(), Read: func(string) (Usage, error) {
		calls++
		free := uint64(1 << 30)
		if calls > 1 {
			free = 100 << 20
		}
		return Usage{TotalBytes: 2 << 30, FreeBytes: free}, nil
	}}
	var target bytes.Buffer
	written, err := Copy(context.Background(), guard, &target, bytes.NewReader(make([]byte, 3<<20)), 0)
	if !errors.Is(err, ErrInsufficient) || written >= 3<<20 {
		t.Fatalf("copy continued after threshold: %d %v", written, err)
	}
}

// TestUsageProbeAndFailure 验证平台实现可用以及探测失败不会放行上传。
func TestUsageProbeAndFailure(t *testing.T) {
	guard := New(t.TempDir())
	if _, err := guard.Usage(); err != nil {
		t.Fatal(err)
	}
	guard.Read = func(string) (Usage, error) { return Usage{}, errors.New("probe failed") }
	if !errors.Is(guard.Check(0), ErrUnavailable) {
		t.Fatal("failed probe must reject upload")
	}
}
