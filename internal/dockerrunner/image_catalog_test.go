package dockerrunner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"stacknest/server/internal/containerapp"
)

// TestImageCleanupGuards 验证当前配置、停止容器、未知镜像和多引用镜像都不能被删除。
func TestImageCleanupGuards(t *testing.T) {
	ids := []string{}
	for _, char := range []string{"a", "b", "c", "d", "e"} {
		ids = append(ids, "sha256:"+strings.Repeat(char, 64))
	}
	var deletes atomic.Int32
	var protected atomic.Bool
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := strings.TrimPrefix(req.URL.Path, "/v1.45")
		switch {
		case path == "/system/df":
			images := []map[string]any{}
			for i, id := range ids {
				tags := []string{"test/image:v" + string(rune('1'+i))}
				if i == 4 {
					tags = append(tags, "other/image:v1")
				}
				images = append(images, map[string]any{"Id": id, "RepoTags": tags, "Size": 1000, "Containers": 0})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"LayersSize": 2500, "Images": images})
		case path == "/containers/json":
			containers := []map[string]string{{"ImageID": ids[1]}}
			if protected.Load() {
				containers = append(containers, map[string]string{"ImageID": ids[0]})
			}
			_ = json.NewEncoder(w).Encode(containers)
		case path == "/images/current:v1/json":
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": ids[2]})
		case req.Method == "DELETE":
			if path != "/images/"+ids[0] || req.URL.Query().Get("force") != "false" || req.URL.Query().Get("noprune") != "true" {
				t.Error("unsafe delete", req.URL)
			}
			deletes.Add(1)
			_, _ = w.Write([]byte("[]"))
		default:
			t.Error("unexpected Docker request", path)
			w.WriteHeader(500)
		}
	}))
	defer engine.Close()
	r := &Runner{engine: fakeEngine(engine), dataDir: t.TempDir()}
	for i, id := range ids {
		if i != 3 {
			r.recordPulledImage(imageInfo{ID: id}, "test/image:v1", map[string]bool{})
		}
	}
	list, err := r.imageInventory(context.Background(), []string{"current:v1"})
	if err != nil || len(list.Images) != 5 || list.TotalBytes != 2500 {
		t.Fatal(list, err)
	}
	for _, image := range list.Images {
		if image.Removable != (image.ID == ids[0]) {
			t.Fatal("incorrect cleanup eligibility", image)
		}
	}
	// 删除时已出现新的外部容器引用，旧清单必须失效。
	protected.Store(true)
	body := `{"id":"` + ids[0] + `","protected":["current:v1"]}`
	response := httptest.NewRecorder()
	r.manageImages(response, httptest.NewRequest("POST", "/images/delete", strings.NewReader(body)))
	if response.Code != 409 || deletes.Load() != 0 {
		t.Fatal(response.Code, deletes.Load())
	}
	protected.Store(false)
	r.mu.Lock()
	response = httptest.NewRecorder()
	r.manageImages(response, httptest.NewRequest("POST", "/images/delete", strings.NewReader(body)))
	r.mu.Unlock()
	if response.Code != 409 || deletes.Load() != 0 {
		t.Fatal("cleanup raced deployment")
	}
	response = httptest.NewRecorder()
	r.manageImages(response, httptest.NewRequest("POST", "/images/delete", strings.NewReader(body)))
	if response.Code != 200 || deletes.Load() != 1 {
		t.Fatal(response.Code, response.Body.String(), deletes.Load())
	}
	if _, exists := r.catalogEntry(ids[0]); exists {
		t.Fatal("deleted image record retained")
	}
}

// TestExistingImagesRemainUnmanaged 验证部署复用已有镜像不能赋予自动清理资格。
func TestExistingImagesRemainUnmanaged(t *testing.T) {
	r := &Runner{dataDir: t.TempDir()}
	id := "sha256:" + strings.Repeat("a", 64)
	for _, before := range []map[string]bool{nil, {id: true}} {
		r.recordPulledImage(imageInfo{ID: id}, "existing:v1", before)
		if _, ok := r.catalogEntry(id); ok {
			t.Fatal("adopted an existing or unknown image")
		}
	}
	if containerapp.ValidImageID("../../other") {
		t.Fatal("unsafe image ID accepted")
	}
}
