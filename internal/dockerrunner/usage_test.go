package dockerrunner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestUsageCounterBoundaries 验证内存缓存扣除、CPU 计数重置和首次采样不会产生假数据。
func TestUsageCounterBoundaries(t *testing.T) {
	var stats resourceStats
	fixture := `{"read":"2026-10-10T13:00:01Z","preread":"2026-10-10T13:00:00Z","cpu_stats":{"cpu_usage":{"total_usage":150},"system_cpu_usage":2000,"online_cpus":4},"precpu_stats":{"cpu_usage":{"total_usage":100},"system_cpu_usage":1000},"memory_stats":{"usage":100,"limit":512,"stats":{"inactive_file":25}},"pids_stats":{"current":7}}`
	if err := json.Unmarshal([]byte(fixture), &stats); err != nil {
		t.Fatal(err)
	}
	result := stats.view()
	if result.CPUPercent == nil || *result.CPUPercent != 20 || result.MemoryBytes != 75 || result.MemoryLimitBytes != 512 || result.PIDs != 7 {
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
	stats.CPU.Usage.Total = 99
	if stats.view().CPUPercent != nil {
		t.Fatal("CPU reset produced bogus percentage")
	}
	stats.CPU.Usage.Total = 150
	stats.PreRead = time.Time{}
	if stats.view().CPUPercent != nil {
		t.Fatal("first sample reported fabricated CPU usage")
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
				if req.URL.Query().Get("stream") != "false" {
					t.Error("unbounded statistics stream requested")
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
