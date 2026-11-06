package dockerrunner

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/containerapp"
)

// TestImageCleanupEngineIntegration 用无网络的临时 scratch 镜像验证真实 Docker 删除语义。
func TestImageCleanupEngineIntegration(t *testing.T) {
	socket := os.Getenv("STACKNEST_TEST_DOCKER_SOCKET")
	if socket == "" {
		t.Skip("explicit Docker test socket required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r := &Runner{engine: NewEngine(socket), dataDir: t.TempDir()}
	before := r.localImageIDs(ctx)
	if before == nil {
		t.Fatal("cannot snapshot engine images")
	}
	dir := t.TempDir()
	tag := "stacknest-cleanup-test-" + uuid.NewString() + ":fixture"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY marker /marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte(tag), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "docker", "-H", "unix://"+socket, "build", "--network=none", "-q", "-t", tag, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, out)
	}
	var info imageInfo
	if err := r.engine.call(ctx, "GET", "/images/"+tag+"/json", nil, &info); err != nil {
		t.Fatal(err)
	}
	if before[info.ID] || !containerapp.ValidImageID(info.ID) {
		t.Fatal("fixture is not isolated")
	}
	defer r.engine.call(context.Background(), "DELETE", "/images/"+info.ID+"?force=false&noprune=true", nil, nil)
	r.recordPulledImage(info, tag, before)
	// call 通过运行器入口执行真实清单和删除，不直接依赖 Docker CLI 的清理行为。
	call := func(path string, input containerapp.ImageRequest) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(input)
		req := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
		w := httptest.NewRecorder()
		r.manageImages(w, req)
		return w
	}
	input := containerapp.ImageRequest{ID: info.ID, Protected: []string{tag}}
	if w := call("/images/delete", input); w.Code != 409 {
		t.Fatal("app reference not protected", w.Code, w.Body.String())
	}
	input.Protected = []string{}
	var created struct {
		ID string `json:"Id"`
	}
	if err := r.engine.call(ctx, "POST", "/containers/create?name="+strings.TrimSuffix(tag, ":fixture"), map[string]any{
		"Image": info.ID, "Cmd": []string{"/not-started"}, "Labels": map[string]string{"com.stacknest.check": "image-cleanup"},
		"HostConfig": map[string]any{"NetworkMode": "none"},
	}, &created); err != nil {
		t.Fatal(err)
	}
	defer r.engine.call(context.Background(), "DELETE", "/containers/"+created.ID, nil, nil)
	if w := call("/images/delete", input); w.Code != 409 {
		t.Fatal("stopped container not protected", w.Code, w.Body.String())
	}
	if err := r.engine.call(ctx, "DELETE", "/containers/"+created.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	w := call("/images/list", input)
	var inventory containerapp.ImageInventory
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &inventory) != nil || inventory.TotalBytes <= 0 {
		t.Fatal("invalid disk usage", w.Code)
	}
	if w := call("/images/delete", input); w.Code != 200 {
		t.Fatal("safe image deletion failed", w.Code, w.Body.String())
	}
	if _, ok := r.catalogEntry(info.ID); ok {
		t.Fatal("removed image record remains")
	}
	after := r.localImageIDs(ctx)
	if after == nil || after[info.ID] {
		t.Fatal("fixture image remains")
	}
	for id := range before {
		if !after[id] {
			t.Fatal("unrelated image changed", id)
		}
	}
}
