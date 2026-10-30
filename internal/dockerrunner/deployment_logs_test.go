package dockerrunner

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/storage"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestDeploymentFailurePersistsRedactedSnapshot 候选移除或运行器重启后仍能读取日志，新配置秘密在保存前已被清除。
func TestDeploymentFailurePersistsRedactedSnapshot(t *testing.T) {
	id, cid := "0c2d84bb-15a1-492b-938a-83282600c112", "aaaaaaaaaaaa"
	dir := t.TempDir()
	r := &Runner{dataDir: dir, instance: "fixture"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": cid, "RestartCount": 7, "Config": map[string]any{"Labels": map[string]string{instanceLabel: "fixture", appLabel: id}}, "HostConfig": map[string]any{"NetworkMode": r.networkName(id)}, "State": map[string]any{"Status": "restarting", "ExitCode": 1}})
		case strings.HasSuffix(req.URL.Path, "/logs"):
			text := []byte("BOOTSTRAP_ADMIN_PASSWORD 必须是 12—128 位密码\nsecret=candidate-private-value registry=private-registry-value\n")
			var header [8]byte
			header[0] = 1
			binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
			_, _ = w.Write(header[:])
			_, _ = w.Write(text)
		default:
			t.Error("unexpected Docker call", req.URL)
		}
	}))
	r.engine = fakeEngine(server)
	op := containerapp.Operation{ID: id, Generation: 3, Spec: containerapp.Spec{Image: "example/app:v3", Port: 3000, HealthPath: "/api/health", Env: map[string]string{"PASSWORD": "candidate-private-value"}, Registry: &containerapp.Registry{Username: "fixture", Password: "private-registry-value"}}}
	reason := r.captureDeploymentFailure(context.Background(), op, cid, errors.New("HTTP 健康检查连接失败"))
	server.Close()
	if !strings.Contains(reason, "7 次") {
		t.Fatal(reason)
	}
	body, err := os.ReadFile(filepath.Join(dir, "apps", id, "last-deployment-failure.json"))
	if err != nil || strings.Contains(string(body), "candidate-private-value") || strings.Contains(string(body), "private-registry-value") {
		t.Fatal("secrets persisted", err)
	}
	restarted := &Runner{dataDir: dir}
	// read 只使用已保存快照，不依赖已不存在的候选容器或 Docker daemon。
	read := func(app string, generation string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/deployment-logs", nil)
		req.SetPathValue("id", app)
		req.SetPathValue("generation", generation)
		out := httptest.NewRecorder()
		restarted.deploymentLogs(out, req)
		return out
	}
	out := read(id, "3")
	if out.Code != 200 || !strings.Contains(out.Body.String(), "BOOTSTRAP_ADMIN_PASSWORD") || !strings.Contains(out.Body.String(), "[已隐藏]") {
		t.Fatal(out.Code, out.Body.String())
	}
	for _, pair := range [][2]string{{id, "2"}, {id, "4"}, {"45c781e3-1f78-4f37-8f7d-0f6c38341eef", "3"}, {"../escape", "3"}} {
		if out := read(pair[0], pair[1]); out.Code != 404 {
			t.Fatal("cross task access accepted", pair, out.Code)
		}
	}
	if err = restarted.saveDeploymentFailure(deploymentFailure{ID: id, Generation: 4, Text: "new failure"}); err != nil {
		t.Fatal(err)
	}
	if read(id, "3").Code != 404 || !strings.Contains(read(id, "4").Body.String(), "new failure") {
		t.Fatal("old generation reused")
	}
}

// TestFailureTextAndFileBounds 对非 UTF-8、长日志、异常文件和编号保持有界且失败关闭。
func TestFailureTextAndFileBounds(t *testing.T) {
	text := boundedFailureText(strings.Repeat("错误\xff", 100000), failureTextLimit)
	if len(text) > failureTextLimit || !utf8.ValidString(text) {
		t.Fatal("invalid bounded text")
	}
	r := &Runner{dataDir: t.TempDir()}
	if err := r.saveDeploymentFailure(deploymentFailure{ID: "../escape", Generation: 1, Text: "bad"}); err == nil {
		t.Fatal("unsafe ID accepted")
	}
	id := "0c2d84bb-15a1-492b-938a-83282600c112"
	if err := r.saveDeploymentFailure(deploymentFailure{ID: id, Generation: 1, Text: strings.Repeat("a", failureTextLimit+1)}); err == nil {
		t.Fatal("unbounded text accepted")
	}
}

// TestFailedDeploymentCapturesBeforeCleanup 走真实部署流程，验证失败日志先落盘再删除候选，并恢复旧容器。
func TestFailedDeploymentCapturesBeforeCleanup(t *testing.T) {
	id, oldID, newID := "0c2d84bb-15a1-492b-938a-83282600c112", "aaaaaaaaaaaa", "cccccccccccc"
	r := &Runner{dataDir: t.TempDir(), instance: "fixture", selfID: "bbbbbbbbbbbb", guard: storage.New(t.TempDir())}
	r.guard.Read = func(string) (storage.Usage, error) {
		return storage.Usage{TotalBytes: 40 << 30, FreeBytes: 30 << 30}, nil
	}
	removed, restored := false, false
	r.engine = &Engine{client: &http.Client{Transport: pullTransport(func(req *http.Request) (*http.Response, error) {
		// respond 模拟受限 Docker 响应，不启动真实容器。
		respond := func(status int, value any) (*http.Response, error) {
			body, _ := json.Marshal(value)
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		}
		switch {
		case strings.Contains(req.URL.Path, "/containers/json"):
			return respond(200, []any{})
		case strings.Contains(req.URL.Path, "/networks/"):
			return respond(200, map[string]any{"Driver": "bridge", "Internal": true, "Labels": map[string]string{instanceLabel: "fixture", appLabel: id}})
		case strings.Contains(req.URL.Path, "/containers/"+r.selfID+"/json"):
			return respond(200, map[string]any{"NetworkSettings": map[string]any{"Networks": map[string]any{r.networkName(id): map[string]any{}}}})
		case strings.Contains(req.URL.Path, "/containers/"+r.candidateName(id, 2)+"/json"):
			return respond(404, nil)
		case strings.Contains(req.URL.Path, "/images/create"):
			return respond(200, map[string]any{"id": "layer", "status": "Pull complete"})
		case strings.Contains(req.URL.Path, "/images/"):
			return respond(200, map[string]any{"Id": "sha256:fixture", "Size": 1024, "Os": "linux", "Architecture": "amd64"})
		case strings.Contains(req.URL.Path, "/containers/create"):
			return respond(200, map[string]any{"Id": newID})
		case strings.Contains(req.URL.Path, "/containers/"+oldID+"/stop"):
			return respond(204, nil)
		case strings.Contains(req.URL.Path, "/containers/"+newID+"/start"):
			return respond(204, nil)
		case strings.Contains(req.URL.Path, "/containers/"+newID+"/json"):
			return respond(200, map[string]any{"Id": newID, "Config": map[string]any{"Labels": map[string]string{instanceLabel: "fixture", appLabel: id}}, "HostConfig": map[string]any{"NetworkMode": r.networkName(id)}, "State": map[string]any{"OOMKilled": true, "Status": "dead", "ExitCode": 137}})
		case strings.Contains(req.URL.Path, "/containers/"+newID+"/logs"):
			message := []byte("startup failed: candidate-secret")
			var header [8]byte
			header[0] = 1
			binary.BigEndian.PutUint32(header[4:], uint32(len(message)))
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(header[:]) + string(message)))}, nil
		case req.Method == "DELETE" && strings.Contains(req.URL.Path, newID):
			body, err := os.ReadFile(filepath.Join(r.dataDir, "apps", id, "last-deployment-failure.json"))
			if err != nil || strings.Contains(string(body), "candidate-secret") {
				t.Error("candidate removed before safe snapshot", err)
			}
			removed = true
			return respond(204, nil)
		case strings.Contains(req.URL.Path, "/containers/"+oldID+"/start"):
			restored = true
			return respond(204, nil)
		default:
			t.Error("unexpected Docker operation", req.Method, req.URL.Path)
			return respond(500, nil)
		}
	})}}
	op := containerapp.Operation{ID: id, Generation: 2, Action: "deploy", Spec: containerapp.Spec{Image: "example/app:v2", MemoryMiB: 128, CPUs: 0.5, Port: 3000, Env: map[string]string{"SECRET": "candidate-secret"}}}
	old := inspection{ID: oldID, Image: "old-image"}
	old.State.Running = true
	old.State.Status = "running"
	value, err := r.deploy(context.Background(), op, old)
	if err == nil || !strings.Contains(err.Error(), "OOM") || value.ContainerID != oldID || !removed || !restored {
		t.Fatal("failure cleanup/rollback lost", value, err, removed, restored)
	}
}
