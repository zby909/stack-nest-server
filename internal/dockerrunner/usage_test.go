package dockerrunner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestUsageReturnsMemoryOnFirstRead 验证首个请求只需单次采样，立即返回内存和进程数。
func TestUsageReturnsMemoryOnFirstRead(t *testing.T) {
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		query := req.URL.Query()
		if query.Get("stream") != "false" || query.Get("one-shot") != "true" {
			t.Error("statistics request waits for more than one sample")
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"read":"2026-11-10T06:00:00Z","memory_stats":{"usage":100,"limit":512,"stats":{"inactive_file":25}},"pids_stats":{"current":7}}`))
	}))
	defer engine.Close()
	runner := &Runner{engine: fakeEngine(engine)}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	usage, err := runner.usage(ctx, "aaaaaaaaaaaa", time.Date(2026, 11, 10, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if usage.MemoryBytes != 75 || usage.MemoryLimitBytes != 512 || usage.PIDs != 7 || usage.CPUPercent != nil || usage.SampledAt.IsZero() {
		t.Fatalf("invalid first sample: %+v", usage)
	}
}

// TestUsageCounterBoundaries 验证兼容两种 cgroup 缓存口径，避免计数下溢。
func TestUsageCounterBoundaries(t *testing.T) {
	var stats resourceStats
	fixture := `{"read":"2026-10-10T13:00:01Z","memory_stats":{"usage":100,"limit":512,"stats":{"inactive_file":25}},"pids_stats":{"current":7}}`
	if err := json.Unmarshal([]byte(fixture), &stats); err != nil {
		t.Fatal(err)
	}
	result := stats.view()
	if result.MemoryBytes != 75 || result.MemoryLimitBytes != 512 || result.PIDs != 7 {
		t.Fatal("incorrect sample", result)
	}
	stats.Memory.Stats = map[string]uint64{"total_inactive_file": 30}
	if stats.view().MemoryBytes != 70 {
		t.Fatal("cgroup v1 cache not deducted")
	}
	stats.Memory.Stats = map[string]uint64{"inactive_file": 101}
	if stats.view().MemoryBytes != 100 {
		t.Fatal("cache counter underflow")
	}
}

// TestCPUUsageAcrossSamples 验证重启、乱序和过期读数不会串到当前容器的 CPU 占用率。
func TestCPUUsageAcrossSamples(t *testing.T) {
	var history cpuHistory
	start := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		name          string
		read, started time.Duration
		total, system uint64
		want          float64
	}{
		{"first", time.Second, 0, 100, 1000, -1},
		{"next", 2 * time.Second, 0, 150, 2000, 20},
		{"restarted", 4 * time.Second, 3 * time.Second, 160, 3000, -1},
		{"late old run", 5 * time.Second, 0, 170, 4000, -1},
		{"new run", 6 * time.Second, 3 * time.Second, 200, 5000, 8},
		{"expired", 60 * time.Second, 3 * time.Second, 300, 6000, -1},
		{"counter reset", 61 * time.Second, 3 * time.Second, 10, 7000, -1},
	} {
		sample := cpuSample{read: start.Add(row.read), startedAt: start.Add(row.started), stats: cpuStats{System: row.system, Online: 4}}
		sample.stats.Usage.Total = row.total
		got := history.percent("container", sample)
		if row.want < 0 && got != nil || row.want >= 0 && (got == nil || *got != row.want) {
			t.Fatalf("%s: unexpected CPU sample %v", row.name, got)
		}
	}
}

// TestRuntimeStatisticsRespectOwnership 先确认应用归属再读取统计，失败时不泄漏原始 Docker 数据。
func TestRuntimeStatisticsRespectOwnership(t *testing.T) {
	const id = "0c2d84bb-15a1-492b-938a-83282600c112"
	for _, foreign := range []bool{false, true} {
		runner := &Runner{instance: "test"}
		requests := 0
		engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if strings.HasSuffix(req.URL.Path, "/stats") {
				requests++
				if req.URL.Query().Get("stream") != "false" || req.URL.Query().Get("one-shot") != "true" {
					t.Error("expected a single statistics sample")
				}
				_, _ = w.Write([]byte(`{"read":"2026-10-10T13:00:01Z","memory_stats":{"usage":100,"limit":512},"pids_stats":{"current":7}}`))
				return
			}
			app := id
			if foreign {
				app = "foreign"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "aaaaaaaaaaaa", "Config": map[string]any{"Env": []string{"SECRET=never-return"}, "Labels": map[string]string{instanceLabel: "test", appLabel: app}},
				"State":      map[string]any{"Running": true, "Status": "running"},
				"HostConfig": map[string]any{"NetworkMode": runner.networkName(id)},
			})
		}))
		runner.engine = fakeEngine(engine)
		req := httptest.NewRequest("GET", "/state/"+id+"/aaaaaaaaaaaa", nil)
		req.SetPathValue("id", id)
		req.SetPathValue("container", "aaaaaaaaaaaa")
		w := httptest.NewRecorder()
		runner.state(w, req)
		engine.Close()
		if foreign {
			if w.Code != 404 || requests != 0 {
				t.Fatal("foreign statistics reached Docker")
			}
		} else if w.Code != 200 || requests != 1 || !strings.Contains(w.Body.String(), `"memoryBytes":100`) {
			t.Fatal("owned statistics missing", w.Body.String())
		}
		if strings.Contains(w.Body.String(), "never-return") {
			t.Fatal("raw Docker response leaked")
		}
	}
}
