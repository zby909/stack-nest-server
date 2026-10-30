package dockerrunner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/storage"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
)

const cancelTestID = "0c2d84bb-15a1-492b-938a-83282600c112"

// TestCancelActivePull 穿过真实镜像流验证取消会关闭下载，且无需取得被长操作持有的锁。
func TestCancelActivePull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		guard := storage.New(t.TempDir())
		guard.Read = func(string) (storage.Usage, error) {
			return storage.Usage{TotalBytes: 40 << 30, FreeBytes: 30 << 30}, nil
		}
		started := make(chan struct{})
		r := &Runner{dataDir: t.TempDir(), guard: guard}
		r.engine = &Engine{client: &http.Client{Transport: pullTransport(func(req *http.Request) (*http.Response, error) {
			if req.Method != "POST" || !strings.Contains(req.URL.Path, "/images/create") {
				t.Error("canceled pull attempted further Docker operations")
			}
			reader, writer := io.Pipe()
			context.AfterFunc(req.Context(), func() { _ = writer.CloseWithError(req.Context().Err()) })
			close(started)
			return &http.Response{StatusCode: 200, Body: reader}, nil
		})}}
		op := containerapp.Operation{ID: cancelTestID, Generation: 3, Action: "deploy"}
		ctx := r.beginProgress(context.Background(), op)
		ctx, finish, err := r.beginOperation(ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		defer finish()
		done := make(chan error, 1)
		go func() { _, err := r.pull(ctx, containerapp.Spec{Image: "example/app:v1"}, ""); done <- err }()
		<-started
		r.mu.Lock()
		response := httptest.NewRecorder()
		r.cancelOperation(response, httptest.NewRequest("POST", "/cancel", strings.NewReader(`{"id":"`+cancelTestID+`","generation":3}`)))
		r.mu.Unlock()
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		if err = <-done; !errors.Is(err, containerapp.ErrDeployCanceled) {
			t.Fatal(err)
		}
		if err = r.sealOperation(ctx); !errors.Is(err, containerapp.ErrDeployCanceled) {
			t.Fatal("cancel did not prevent container switching", err)
		}
		r.finishProgress(ctx, err)
		if r.progress[cancelTestID].Stage != "cancelled" || r.progress[cancelTestID].CanCancel {
			t.Fatal(r.progress[cancelTestID])
		}
	})
}

// TestCancellationSurvivesRestart 已确认的排队取消在新运行器中继续生效，新代次不受影响。
func TestCancellationSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{dataDir: dir}
	if err := r.requestCancellation(cancelTestID, 5); err != nil {
		t.Fatal(err)
	}
	if err := r.requestCancellation(cancelTestID, 5); err != nil {
		t.Fatal("repeated cancellation not idempotent", err)
	}
	restarted := &Runner{dataDir: dir}
	for _, generation := range []int64{4, 5, 6} {
		op := containerapp.Operation{ID: cancelTestID, Generation: generation, Action: "deploy"}
		ctx, finish, err := restarted.beginOperation(context.Background(), op)
		if generation <= 5 && (!errors.Is(err, containerapp.ErrDeployCanceled) || !errors.Is(context.Cause(ctx), containerapp.ErrDeployCanceled)) {
			t.Fatal(generation, err)
		}
		if generation == 6 && err != nil {
			t.Fatal("new deployment was canceled", err)
		}
		finish()
	}
}

// TestCancelIdentityAndSwitchingBoundary 验证旧代次、其他应用和切换后的请求均不会取消当前任务。
func TestCancelIdentityAndSwitchingBoundary(t *testing.T) {
	r := &Runner{dataDir: t.TempDir()}
	op := containerapp.Operation{ID: cancelTestID, Generation: 3, Action: "deploy"}
	ctx := r.beginProgress(context.Background(), op)
	ctx, finish, err := r.beginOperation(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	for _, generation := range []int64{2, 4} {
		if err = r.requestCancellation(cancelTestID, generation); !errors.Is(err, containerapp.ErrBusy) {
			t.Fatal(generation, err)
		}
	}
	other := "45c781e3-1f78-4f37-8f7d-0f6c38341eef"
	if err = r.requestCancellation(other, 1); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("another application interrupted active task")
	}
	if err = r.sealOperation(ctx); err != nil {
		t.Fatal(err)
	}
	if err = r.requestCancellation(cancelTestID, 3); !errors.Is(err, containerapp.ErrCancelTooLate) {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("switching deployment was interrupted")
	}
	if value, err := r.canceledGeneration(cancelTestID); err != nil || value != 0 {
		t.Fatal("rejected request persisted", value, err)
	}
	for _, input := range []string{"../escape", "", strings.Repeat("a", 500)} {
		if err = r.requestCancellation(input, 3); !errors.Is(err, containerapp.ErrNotFound) {
			t.Fatal(input, err)
		}
	}
}

// TestCancelStorageFailureDoesNotAcknowledge 磁盘无法保存取消请求时不返回成功，也不触发仅存在于内存的取消。
func TestCancelStorageFailureDoesNotAcknowledge(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{dataDir: dir}
	op := containerapp.Operation{ID: cancelTestID, Generation: 1, Action: "deploy"}
	ctx := r.beginProgress(context.Background(), op)
	ctx, finish, err := r.beginOperation(ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if err = os.WriteFile(filepath.Join(dir, "apps"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = r.requestCancellation(cancelTestID, 1); err == nil {
		t.Fatal("storage failure acknowledged")
	}
	if ctx.Err() != nil {
		t.Fatal("unconfirmed cancellation interrupted deployment")
	}
}

// TestCancelAndSwitchAreMutuallyExclusive 并发点击取消与容器切换只能有一个成功，不能出现取消已确认却继续切换。
func TestCancelAndSwitchAreMutuallyExclusive(t *testing.T) {
	for range 20 {
		r := &Runner{dataDir: t.TempDir()}
		op := containerapp.Operation{ID: cancelTestID, Generation: 7, Action: "deploy"}
		ctx := r.beginProgress(context.Background(), op)
		ctx, finish, err := r.beginOperation(ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wait sync.WaitGroup
		var cancelErr, switchErr error
		wait.Add(2)
		go func() { defer wait.Done(); <-start; cancelErr = r.requestCancellation(cancelTestID, 7) }()
		go func() { defer wait.Done(); <-start; switchErr = r.sealOperation(ctx) }()
		close(start)
		wait.Wait()
		if cancelErr == nil {
			if !errors.Is(switchErr, containerapp.ErrDeployCanceled) {
				t.Fatal("confirmed cancellation allowed switching", switchErr)
			}
		} else if !errors.Is(cancelErr, containerapp.ErrCancelTooLate) || switchErr != nil {
			t.Fatal(cancelErr, switchErr)
		}
		finish()
	}
}
