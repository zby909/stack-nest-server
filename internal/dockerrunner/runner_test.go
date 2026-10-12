package dockerrunner

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"stacknest/server/internal/containerapp"
	"strings"
	"testing"
)

// fakeEngine 将 Docker API 定向到测试服务器，绝不操作真实 daemon。
func fakeEngine(server *httptest.Server) *Engine {
	return &Engine{client: &http.Client{Transport: rewriteTransport{server: server.URL, base: http.DefaultTransport}}}
}

type rewriteTransport struct {
	server string
	base   http.RoundTripper
}

// RoundTrip 仅在测试中改写固定的 Docker 来源。
func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	url := *req.URL
	clone.URL = &url
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(r.server, "http://")
	return r.base.RoundTrip(clone)
}

// TestConstrainedCreate 检查容器资源、网络和 bind 均由平台生成，用户没有任意宿主路径。
func TestConstrainedCreate(t *testing.T) {
	var payload map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/v1.45/containers/create") {
			t.Errorf("unexpected %s", r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"Id":"aaaaaaaaaaaa"}`))
	}))
	defer server.Close()
	root := t.TempDir()
	runner := &Runner{engine: fakeEngine(server), instance: "test", source: "/docker/volumes/stacknest-data/_data", dataDir: root}
	id := "0c2d84bb-15a1-492b-938a-83282600c112"
	op := containerapp.Operation{ID: id, Generation: 1, Spec: containerapp.Spec{Image: "example/app:v1", Port: 3000, CPUs: 0.5, MemoryMiB: 1024, DataPath: "/app/data"}}
	if _, err := runner.create(context.Background(), op, imageInfo{ID: "sha256:abc"}); err != nil {
		t.Fatal(err)
	}
	var host struct {
		Privileged                              bool
		NetworkMode                             string
		Memory, MemorySwap, NanoCpus, PidsLimit int64
		SecurityOpt, CapDrop                    []string
		Binds                                   []string
		PortBindings                            map[string]any
		Mounts                                  []struct{ Type, Source, Target string }
	}
	if err := json.Unmarshal(payload["HostConfig"], &host); err != nil {
		t.Fatal(err)
	}
	if host.Privileged || host.Memory != 1024<<20 || host.MemorySwap != host.Memory || host.NanoCpus != 5e8 || host.PidsLimit != 128 || host.NetworkMode != runner.networkName(id) || len(host.PortBindings) != 0 || len(host.Binds) != 0 {
		t.Fatalf("unsafe host config: %+v", host)
	}
	if len(host.Mounts) != 1 || host.Mounts[0].Target != "/app/data" || host.Mounts[0].Source != filepath.Join(runner.source, "apps", id, "data") {
		t.Fatal("bad mount", host.Mounts)
	}
	if len(host.SecurityOpt) != 1 || host.SecurityOpt[0] != "no-new-privileges:true" {
		t.Fatal("privilege escalation permitted")
	}
}

// TestOwnershipRejectsPlatformContainers 不允许使用平台或其他应用容器编号查看、删除和代理。
func TestOwnershipRejectsPlatformContainers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Id":"aaaaaaaaaaaa","Config":{"Labels":{"io.stacknest.instance":"test","io.stacknest.app":"foreign"}},"HostConfig":{"NetworkMode":"host"}}`))
	}))
	defer server.Close()
	runner := &Runner{engine: fakeEngine(server), instance: "test"}
	if _, err := runner.owned(context.Background(), "0c2d84bb-15a1-492b-938a-83282600c112", "aaaaaaaaaaaa"); err == nil {
		t.Fatal("platform or foreign container accepted")
	}
}

// TestLogFrames 验证交错 stdout/stderr 解帧与超长帧拒绝。
func TestLogFrames(t *testing.T) {
	var raw bytes.Buffer
	for i, text := range []string{"stdout\n", "stderr\n"} {
		header := make([]byte, 8)
		header[0] = byte(i + 1)
		binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
		raw.Write(header)
		raw.WriteString(text)
	}
	text, err := readLogs(&raw)
	if err != nil || text != "stdout\nstderr\n" {
		t.Fatal(text, err)
	}
	header := make([]byte, 8)
	binary.BigEndian.PutUint32(header[4:], 1<<30)
	if _, err = readLogs(bytes.NewReader(header)); err == nil {
		t.Fatal("huge log frame accepted")
	}
}

// TestRejectExtraVolumes 限制镜像体积、架构与隐式数据卷。
func TestRejectExtraVolumes(t *testing.T) {
	r := &Runner{}
	value := imageInfo{Os: "linux", Architecture: "amd64", Size: 100}
	spec := containerapp.Spec{DataPath: "/app/data"}
	if err := r.validateImage(value, spec); err != nil {
		t.Fatal(err)
	}
	value.Config.Volumes = map[string]any{"/var/lib/db": nil}
	if err := r.validateImage(value, spec); err == nil {
		t.Fatal("anonymous volume accepted")
	}
	value.Config.Volumes = nil
	value.Size = containerapp.MaxImageBytes + 1
	if err := r.validateImage(value, spec); err == nil {
		t.Fatal("large image accepted")
	}
}

// TestFreezeRetriesPartialFailure 确认失败后重试仍检查并停止剩余应用，不能误报备份安全。
func TestFreezeRetriesPartialFailure(t *testing.T) {
	id, cid := "0c2d84bb-15a1-492b-938a-83282600c112", "aaaaaaaaaaaa"
	running, stops := true, 0
	runner := &Runner{instance: "test", dataDir: t.TempDir()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.Contains(req.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": cid, "State": "running", "Labels": map[string]string{appLabel: id}}})
		case strings.HasSuffix(req.URL.Path, "/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": cid, "State": map[string]any{"Running": running}, "Config": map[string]any{"Labels": map[string]string{instanceLabel: "test", appLabel: id}}, "HostConfig": map[string]any{"NetworkMode": runner.networkName(id)}})
		case strings.Contains(req.URL.Path, "/stop"):
			stops++
			if stops == 1 {
				w.WriteHeader(500)
				return
			}
			running = false
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s", req.URL)
		}
	}))
	defer server.Close()
	runner.engine = fakeEngine(server)
	for i, expected := range []int{503, 200} {
		response := httptest.NewRecorder()
		runner.maintenance(response, httptest.NewRequest("POST", "/maintenance", strings.NewReader(`{"frozen":true}`)))
		if response.Code != expected {
			t.Fatalf("attempt %d status %d", i, response.Code)
		}
	}
	if running || stops != 2 || !runner.frozen {
		t.Fatal("freeze did not retry remaining application")
	}
	if _, err := os.Stat(filepath.Join(runner.dataDir, "app-maintenance.json")); err != nil {
		t.Fatal(err)
	}
}

// TestOperationEscapedConfiguration 验证内部客户端编码的合法最大环境配置能进入操作处理阶段。
func TestOperationEscapedConfiguration(t *testing.T) {
	op := containerapp.Operation{ID: "0c2d84bb-15a1-492b-938a-83282600c112", Generation: 1,
		Action: "deploy", Spec: containerapp.Spec{Image: "example/app:v1", Port: 3000, CPUs: 0.5,
			MemoryMiB: 128, Env: map[string]string{"A": strings.Repeat("<", 4095),
				"B": strings.Repeat("&", 4095), "C": strings.Repeat(">", 4095), "D": strings.Repeat("<", 4095)},
			Registry: &containerapp.Registry{Username: "user", Password: strings.Repeat("&", 4096)}}}
	if err := op.Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 32768 {
		t.Fatal("fixture did not exercise JSON expansion")
	}
	runner := &Runner{frozen: true}
	out := httptest.NewRecorder()
	runner.operation(out, httptest.NewRequest("POST", "/operation", bytes.NewReader(raw)))
	if out.Code != 503 {
		t.Fatalf("legal request rejected before maintenance guard: %d", out.Code)
	}
}
