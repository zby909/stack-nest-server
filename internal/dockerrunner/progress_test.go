package dockerrunner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/storage"
	"testing"
	"time"
)

// TestProgressGenerationAndRecovery 确认旧代次、旧任务更新及恢复阶段不会误导当前任务。
func TestProgressGenerationAndRecovery(t *testing.T) {
	r := &Runner{}
	op := containerapp.Operation{ID: "0c2d84bb-15a1-492b-938a-83282600c112", Generation: 1}
	old := r.beginProgress(context.Background(), op)
	r.stage(old, "pulling")
	op.Generation = 2
	current := r.beginProgress(context.Background(), op)
	r.download(old, 100, 200)
	r.stage(old, "failed")
	r.stage(current, "healthchecking")
	r.stage(current, "restoring")
	r.finishProgress(current, errors.New("test"))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /progress/{id}/{generation}", r.readProgress)
	for _, generation := range []string{"1", "2", "bad"} {
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, httptest.NewRequest("GET", "/progress/"+op.ID+"/"+generation, nil))
		if generation != "2" {
			if out.Code != 404 {
				t.Fatal("old or invalid generation accepted")
			}
			continue
		}
		var value containerapp.Progress
		if json.Unmarshal(out.Body.Bytes(), &value) != nil || value.Stage != "failed" || value.FailedStage != "healthchecking" || value.DownloadedBytes != 0 {
			t.Fatal("stale or incorrect progress", out.Body.String())
		}
	}
}

// TestPullProgressDoesNotBlockBehindOperation 用阻塞拉取流验证进度可以查询且仓库原文不泄漏。
func TestPullProgressDoesNotBlockBehindOperation(t *testing.T) {
	sent, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	docker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1.45/images/create" {
			_, _ = w.Write([]byte(`{"id":"layer-1","status":"Downloading","progressDetail":{"current":50,"total":100}}` + "\n"))
			w.(http.Flusher).Flush()
			close(sent)
			select {
			case <-release:
			case <-req.Context().Done():
			}
			_, _ = w.Write([]byte(`{"error":"do-not-leak-test-registry-password"}` + "\n"))
			return
		}
		t.Errorf("unexpected request: %s", req.URL.Path)
	}))
	defer docker.Close()
	r := &Runner{engine: fakeEngine(docker), guard: storage.New(t.TempDir())}
	op := containerapp.Operation{ID: "0c2d84bb-15a1-492b-938a-83282600c112", Generation: 3}
	base, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx := r.beginProgress(base, op)
	finished := make(chan error, 1)
	go func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		_, err := r.pull(ctx, containerapp.Spec{Image: "example/app:v1"}, "")
		finished <- err
	}()
	select {
	case <-sent:
	case <-base.Done():
		t.Fatal("no pull stream")
	}
	deadline := time.Now().Add(time.Second)
	for {
		r.progressMu.Lock()
		value := r.progress[op.ID]
		r.progressMu.Unlock()
		if value.DownloadedBytes == 50 && value.TotalBytes == 100 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("download progress not updated")
		}
		time.Sleep(time.Millisecond)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /progress/{id}/{generation}", r.readProgress)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(server.URL + "/progress/" + op.ID + "/3")
	if err != nil {
		t.Fatal("operation blocked progress", err)
	}
	defer response.Body.Close()
	var value containerapp.Progress
	if json.NewDecoder(response.Body).Decode(&value) != nil || value.Stage != "pulling" || value.DownloadedBytes != 50 {
		t.Fatal("incorrect progress")
	}
	cancel()
	if err := <-finished; err == nil {
		t.Fatal("interrupted pull reported success")
	}
}

// TestPullLayerAggregation 验证多层汇总、缓存层、完成通知和异常数值不会虚构整体百分比。
func TestPullLayerAggregation(t *testing.T) {
	layers := map[string]pullLayer{}
	event := pullEvent{ID: "a", Status: "Downloading"}
	event.ProgressDetail.Current, event.ProgressDetail.Total = 20, 100
	current, total := recordPull(layers, event)
	if current != 20 || total != 100 {
		t.Fatal(current, total)
	}
	event.ID = "b"
	event.ProgressDetail.Current, event.ProgressDetail.Total = 30, 60
	current, total = recordPull(layers, event)
	if current != 50 || total != 160 {
		t.Fatal(current, total)
	}
	event.ID, event.Status = "a", "Download complete"
	current, total = recordPull(layers, event)
	if current != 130 || total != 160 {
		t.Fatal(current, total)
	}
	event.ID, event.Status = "cache", "Already exists"
	event.ProgressDetail.Total = -1
	current, total = recordPull(layers, event)
	if current != 130 || total != 160 {
		t.Fatal("invalid or cached layer inflated size")
	}
}

// TestCapacityAndRequestedBudgets 检查系统预留与运行器预算校验，后台不能要求无限额。
func TestCapacityAndRequestedBudgets(t *testing.T) {
	if capacityBudget(1600<<20) != 768 || capacityBudget(4096<<20) != 3072 {
		t.Fatal("host reservation changed")
	}
	r := &Runner{maxTotalMemoryMiB: 1024}
	for _, value := range []containerapp.Limits{{MaxApps: 5, TotalMemoryMiB: 1024}, {MaxApps: 0, TotalMemoryMiB: 768}, {MaxApps: 3, TotalMemoryMiB: 1025}} {
		_, err := r.operationLimits(containerapp.Operation{Limits: &value})
		if (err == nil) != (value.MaxApps == 5) {
			t.Fatalf("incorrect validation: %+v %v", value, err)
		}
	}
}
