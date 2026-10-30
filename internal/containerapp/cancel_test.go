package containerapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"stacknest/server/internal/storage"
	"strings"
	"testing"
)

type cancelRuntime struct {
	testRuntime
	canceledID         string
	canceledGeneration int64
	cancelErr          error
}

// Cancel 记录真实传入的应用与代次，供权限及版本测试断言。
func (r *cancelRuntime) Cancel(_ context.Context, id string, generation int64) error {
	r.canceledID, r.canceledGeneration = id, generation
	return r.cancelErr
}

type privateCancelStore struct{ *testStore }

// App 模拟数据库拒绝读取其他用户的应用。
func (*privateCancelStore) App(context.Context, string, Actor) (App, error) {
	return App{}, ErrNotFound
}

// TestCancelPendingRestoresActive 取消排队更新后恢复活动配置及预算，并允许继续编辑。
func TestCancelPendingRestoresActive(t *testing.T) {
	id, secret := "0c2d84bb-15a1-492b-938a-83282600c112", "test-secret-for-app-cancellation"
	active, _ := seal(secret, id, testSpec())
	next := testSpec()
	next.MemoryMiB, next.Image = 512, "example/app:v2"
	sealed, _ := seal(secret, id, next)
	for _, withActive := range []bool{false, true} {
		store := &testStore{value: App{ID: id, Generation: 3, Status: "pending", Action: "deploy", Sealed: sealed, MemoryMiB: 512, ContainerID: "old"}}
		if withActive {
			store.value.ActiveSealed = active
		}
		runtime := &cancelRuntime{}
		service := New(store, runtime, secret, storage.New(t.TempDir()), nil)
		value, err := service.Queue(context.Background(), id, Actor{}, "cancel", 3)
		if err != nil || !value.Canceled || value.Status != "error" || value.Action != "" {
			t.Fatal(value, err)
		}
		if runtime.canceledID != id || runtime.canceledGeneration != 3 || store.value.Generation != 3 || store.value.ContainerID != "old" {
			t.Fatal("wrong generation or container changed")
		}
		if withActive && (store.value.Sealed != active || store.value.MemoryMiB != 128) {
			t.Fatal("active settings not restored")
		}
		if !withActive && store.value.Sealed != sealed {
			t.Fatal("initial configuration lost")
		}
	}
}

// TestCancelChecksOwnershipVersionAndOutcome 在运行器收到请求前拒绝越权、旧代次和已完成任务。
func TestCancelChecksOwnershipVersionAndOutcome(t *testing.T) {
	id, secret := "0c2d84bb-15a1-492b-938a-83282600c112", "test-secret-for-app-cancellation"
	sealed, _ := seal(secret, id, testSpec())
	base := App{ID: id, Generation: 3, Status: "deploying", Action: "deploy", Sealed: sealed}
	for _, scenario := range []string{"foreign", "stale", "finished", "stop", "too-late", "active"} {
		t.Run(scenario, func(t *testing.T) {
			store := &testStore{value: base}
			runtime := &cancelRuntime{}
			service := New(store, runtime, secret, storage.New(t.TempDir()), nil)
			generation := int64(3)
			switch scenario {
			case "foreign":
				service.store = &privateCancelStore{store}
			case "stale":
				generation = 2
			case "finished":
				store.value.Status, store.value.Action = "ready", ""
			case "stop":
				store.value.Action = "stop"
			case "too-late":
				runtime.cancelErr = ErrCancelTooLate
			}
			_, err := service.Cancel(context.Background(), id, Actor{}, generation)
			if scenario == "active" {
				if err != nil || store.value.Status != "deploying" {
					t.Fatal("active task was prematurely marked completed", err)
				}
			} else if err == nil {
				t.Fatal("invalid cancellation accepted")
			}
			if scenario == "foreign" && !errors.Is(err, ErrNotFound) {
				t.Fatal(err)
			}
			if scenario == "too-late" && !errors.Is(err, ErrCancelTooLate) {
				t.Fatal(err)
			}
			if scenario != "active" && scenario != "too-late" && runtime.canceledID != "" {
				t.Fatal("rejected request reached runner")
			}
		})
	}
}

// TestClientCancellationProtocol 核对独立控制请求的编号和代次，保留安全的阶段冲突且屏蔽底层错误。
func TestClientCancellationProtocol(t *testing.T) {
	for _, message := range []string{"", ErrBusy.Error(), ErrCancelTooLate.Error(), "internal network detail"} {
		client := &Client{client: &http.Client{Transport: timeoutTransport(func(req *http.Request) (*http.Response, error) {
			var value struct {
				ID         string `json:"id"`
				Generation int64  `json:"generation"`
			}
			if req.Method != "POST" || req.URL.Path != "/cancel" || json.NewDecoder(req.Body).Decode(&value) != nil || value.ID != "0c2d84bb-15a1-492b-938a-83282600c112" || value.Generation != 9 {
				t.Fatal("cancellation identity not transmitted")
			}
			if message == "" {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"accepted":true}`))}, nil
			}
			body, _ := json.Marshal(map[string]string{"error": message})
			return &http.Response{StatusCode: 409, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		})}}
		err := client.Cancel(context.Background(), "0c2d84bb-15a1-492b-938a-83282600c112", 9)
		want := map[string]error{"": nil, ErrBusy.Error(): ErrBusy, ErrCancelTooLate.Error(): ErrCancelTooLate, "internal network detail": ErrUnavailable}[message]
		if !errors.Is(err, want) {
			t.Fatal(message, err)
		}
	}
}
