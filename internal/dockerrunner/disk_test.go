package dockerrunner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"stacknest/server/internal/containerapp"
	"sync"
	"testing"
	"time"
)

// fakeQuotaReader 用固定计量验证缓存和目录绑定，无须本机 Docker。
type fakeQuotaReader struct {
	mu    sync.Mutex
	calls int
	used  uint64
}

// Ensure 为测试提供无副作用的接口占位。
func (q *fakeQuotaReader) Ensure(string, string) error { return nil }

// Remove 为测试提供无副作用的接口占位。
func (q *fakeQuotaReader) Remove(string, string) error { return nil }

// Usage 模拟平台数据卷的 XFS 计量。
func (q *fakeQuotaReader) Usage(string, string) (containerapp.DiskArea, error) {
	return q.WritableUsage("", "")
}

// WritableUsage 只递增计数，不访问真实文件系统。
func (q *fakeQuotaReader) WritableUsage(string, string) (containerapp.DiskArea, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	limit := uint64(1 << 30)
	used := q.used
	return containerapp.DiskArea{UsedBytes: &used, LimitBytes: &limit, Source: "xfs"}, nil
}

// TestDiskSamplingIsCachedAndBoundToContainer 覆盖首次未知、并发缓存和容器更新不复用旧计数。
func TestDiskSamplingIsCachedAndBoundToContainer(t *testing.T) {
	q := &fakeQuotaReader{used: 64 << 20}
	r := &Runner{quota: q}
	value := inspection{ID: "first"}
	for range 20 {
		r.diskUsage("application", value)
	}
	deadline := time.Now().Add(time.Second)
	var sample *containerapp.DiskUsage
	for time.Now().Before(deadline) {
		sample = r.diskUsage("application", value)
		if sample != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if sample == nil || sample.Writable.UsedBytes == nil || *sample.Writable.UsedBytes != 64<<20 {
		t.Fatal("missing quota sample")
	}
	for range 20 {
		r.diskUsage("application", value)
	}
	q.mu.Lock()
	calls := q.calls
	q.mu.Unlock()
	if calls != 1 {
		t.Fatal("uncached sampling", calls)
	}
	if sample.Persistent != nil {
		t.Fatal("invented persistent directory")
	}
	if r.diskUsage("application", inspection{ID: "replacement"}) != nil {
		t.Fatal("replacement reused stale counts")
	}
}

// TestUnknownDiskUsageRemainsUnknown Docker 错误不应被报告为零空间占用。
func TestUnknownDiskUsageRemainsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer server.Close()
	engine := fakeEngine(server)
	r := &Runner{engine: engine}
	sample := r.sampleDisk("application", inspection{ID: "broken"})
	raw, _ := json.Marshal(sample)
	if sample.Writable.UsedBytes != nil || sample.Writable.Source != "unavailable" {
		t.Fatal(string(raw))
	}
}
