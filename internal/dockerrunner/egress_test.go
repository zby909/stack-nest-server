package dockerrunner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePolicy 为边界测试写入与宿主一致的只读状态格式。
func writePolicy(t *testing.T, path string, value egressStatus) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// validPolicy 提供可修改的真实状态结构，避免仅测试实现细节。
func validPolicy() egressStatus {
	return egressStatus{Version: 1, Ready: true, CheckedAt: time.Now(), BridgePrefix: egressBridgePrefix, BlockedCIDRs: []string{"192.168.2.0/24", "fe80::/64"}, DNSServers: []string{"1.1.1.1"}}
}

// TestEgressBlocksStartupWithoutFreshPolicy 确认策略异常不会触发任何 Docker 启动或网络修改。
func TestEgressBlocksStartupWithoutFreshPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer server.Close()
	runner := &Runner{engine: fakeEngine(server), egressFile: path, instance: "test"}
	cases := []struct {
		name   string
		change func(*egressStatus)
	}{
		{"stale", func(v *egressStatus) { v.CheckedAt = time.Now().Add(-time.Minute) }},
		{"future", func(v *egressStatus) { v.CheckedAt = time.Now().Add(time.Minute) }},
		{"not-ready", func(v *egressStatus) { v.Ready = false }},
		{"wrong-prefix", func(v *egressStatus) { v.BridgePrefix = "br-" }},
		{"no-LAN", func(v *egressStatus) { v.BlockedCIDRs = nil }},
		{"all-internet", func(v *egressStatus) { v.BlockedCIDRs = []string{"0.0.0.0/0"} }},
		{"router-DNS", func(v *egressStatus) { v.DNSServers = []string{"192.168.2.1"} }},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			value := validPolicy()
			item.change(&value)
			writePolicy(t, path, value)
			if err := runner.ensureNetwork(context.Background(), "0c2d84bb-15a1-492b-938a-83282600c112"); err == nil {
				t.Fatal("invalid policy accepted")
			}
			if err := runner.start(context.Background(), "aaaaaaaaaaaa"); err == nil {
				t.Fatal("container started without policy")
			}
		})
	}
	if calls != 0 {
		t.Fatal("policy failure mutated Docker", calls)
	}
	writePolicy(t, path, validPolicy())
	if err := runner.checkEgress(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := runner.checkEgress(); err == nil {
		t.Fatal("missing policy accepted")
	}
}

// TestProtectedNetwork 校验独立网桥归属与 Runner 网关优先级，旧网络必须显式迁移。
func TestProtectedNetwork(t *testing.T) {
	id := "0c2d84bb-15a1-492b-938a-83282600c112"
	path := filepath.Join(t.TempDir(), "status.json")
	writePolicy(t, path, validPolicy())
	runner := &Runner{instance: "test", selfID: "aaaaaaaaaaaa", egressFile: path}
	old := false
	createCount := 0
	connectCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1.48/") {
			t.Error("gateway-capable API not used")
		}
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/networks/"):
			if !old {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Driver": "bridge", "Internal": true, "Labels": map[string]string{instanceLabel: "test", appLabel: id}})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/networks/create"):
			var v struct {
				Name, Driver    string
				Internal        bool
				Options, Labels map[string]string
			}
			_ = json.NewDecoder(r.Body).Decode(&v)
			if v.Internal || v.Name != runner.networkName(id) || v.Driver != "bridge" || v.Options["com.docker.network.bridge.name"] != runner.bridgeName(id) || v.Labels[appLabel] != id {
				t.Fatal("unsafe network", v)
			}
			createCount++
			_, _ = w.Write([]byte("{}"))
		case strings.Contains(r.URL.Path, "/containers/"):
			_, _ = w.Write([]byte(`{"NetworkSettings":{"Networks":{}}}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/connect"):
			var v struct {
				Container      string
				EndpointConfig struct{ GwPriority int }
			}
			_ = json.NewDecoder(r.Body).Decode(&v)
			if v.Container != runner.selfID || v.EndpointConfig.GwPriority != -1 {
				t.Fatal("runner default gateway changed")
			}
			connectCount++
			w.WriteHeader(200)
		default:
			t.Error("unexpected request", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	runner.engine = fakeEngine(server)
	runner.engine.version = "1.48"
	if err := runner.ensureNetwork(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	old = true
	if err := runner.ensureNetwork(context.Background(), id); err == nil {
		t.Fatal("legacy network silently opened")
	}
	if createCount != 1 || connectCount != 1 {
		t.Fatal("network ownership failure mutated network")
	}
	if len(runner.bridgeName(id)) > 15 || runner.bridgeName(id) == (&Runner{instance: "another"}).bridgeName(id) {
		t.Fatal("bridge names collide")
	}
}
