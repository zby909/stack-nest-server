package containerapp

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"stacknest/server/internal/storage"
)

// testSpec 创建可用于测试的受限配置。
func testSpec() Spec {
	return Spec{Image: "docker.io/example/app:v1", Port: 3000, MemoryMiB: 128, CPUs: 0.5, DataPath: "/app/data", Env: map[string]string{"SECRET": "hidden-test-value"}}
}

// TestSpecBoundaries 拒绝镜像注入、危险目录、资源越界与异常环境变量。
func TestSpecBoundaries(t *testing.T) {
	valid := []string{"nginx:alpine", "ghcr.io/example/app:v1", "registry.example.com:5000/team/app@sha256:" + strings.Repeat("a", 64)}
	for _, image := range valid {
		spec := testSpec()
		spec.Image = image
		if err := spec.Validate(); err != nil {
			t.Fatal(image, err)
		}
	}
	for _, memory := range []int{64, 256, 768, 1024} {
		spec := testSpec()
		spec.MemoryMiB = memory
		if err := spec.Validate(); err != nil {
			t.Fatal("valid application memory rejected", memory, err)
		}
	}
	cases := []func(*Spec){
		func(s *Spec) { s.Image = "https://registry/app:v1" },
		func(s *Spec) { s.Image = "repo/app;rm -rf /" },
		func(s *Spec) { s.MemoryMiB = 63 }, func(s *Spec) { s.MemoryMiB = 1025 },
		func(s *Spec) { s.CPUs = math.NaN() },
		func(s *Spec) { s.Port = 0 }, func(s *Spec) { s.DataPath = "/var/run/docker.sock" },
		func(s *Spec) { s.DataPath = "/app/../../data" }, func(s *Spec) { s.DataPath = "/etc/" },
		func(s *Spec) { s.Env = map[string]string{"BAD=KEY": "value"} },
		func(s *Spec) { s.Env = map[string]string{"KEY": strings.Repeat("x", 4097)} },
		func(s *Spec) { s.Registry = &Registry{Username: "user"} },
	}
	for i, change := range cases {
		spec := testSpec()
		change(&spec)
		if err := spec.Validate(); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

// TestSecretsBindIdentity 验证密文的不可回显、篡改检查与身份绑定。
func TestSecretsBindIdentity(t *testing.T) {
	secret, id := "thirty-two-byte-test-secret-only", "0c2d84bb-15a1-492b-938a-83282600c112"
	spec := testSpec()
	spec.Registry = &Registry{Username: "test", Password: "registry-only-secret"}
	sealed, err := seal(secret, id, spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, spec.Env["SECRET"]) || strings.Contains(sealed, spec.Registry.Password) {
		t.Fatal("plaintext leaked")
	}
	decoded, err := open(secret, id, sealed)
	if err != nil || decoded.Env["SECRET"] != spec.Env["SECRET"] {
		t.Fatal("roundtrip", err)
	}
	for _, attempt := range []struct{ key, id, value string }{{"wrong", id, sealed}, {secret, "another-id", sealed}, {secret, id, sealed[:len(sealed)-1] + "!"}} {
		if _, err := open(attempt.key, attempt.id, attempt.value); err == nil {
			t.Fatal("accepted invalid ciphertext")
		}
	}
	raw, _ := json.Marshal(App{Sealed: sealed, ActiveSealed: sealed})
	if strings.Contains(string(raw), "sealed") || strings.Contains(string(raw), sealed) {
		t.Fatal("public app leaks ciphertext")
	}
}

type testStore struct {
	value     App
	completed App
	result    Result
	message   string
}

// Apps 提供测试应用列表。
func (s *testStore) Apps(context.Context, Actor) ([]App, error) { return []App{s.value}, nil }

// App 提供测试应用记录。
func (s *testStore) App(context.Context, string, Actor) (App, error) { return s.value, nil }

// SaveApp 记录加密配置。
func (s *testStore) SaveApp(_ context.Context, a App, _ Actor, _ bool) error { s.value = a; return nil }

// QueueApp 模拟任务排队。
func (s *testStore) QueueApp(context.Context, string, Actor, string, int64) (App, error) {
	return s.value, nil
}

// RecoverApps 模拟启动恢复。
func (s *testStore) RecoverApps(context.Context) error { return nil }

// ClaimApp 模拟空队列。
func (s *testStore) ClaimApp(context.Context) (App, error) { return App{}, ErrNotFound }

// CompleteApp 保存任务结果，供失败恢复断言。
func (s *testStore) CompleteApp(_ context.Context, a App, result Result, message string) error {
	s.completed = a
	s.result = result
	s.message = message
	return nil
}

// CancelPendingApp 模拟只结束排队中的当前部署。
func (s *testStore) CancelPendingApp(_ context.Context, a App) error {
	if s.value.Generation == a.Generation && s.value.Status == "pending" && s.value.Action == "deploy" {
		s.value.Sealed, s.value.MemoryMiB = a.Sealed, a.MemoryMiB
		s.value.Status, s.value.Action, s.value.Error = "error", "", ErrDeployCanceled.Error()
	}
	return nil
}

type testRuntime struct {
	fail   bool
	calls  int
	cancel context.CancelFunc
}

// Health 模拟可用运行器。
func (r *testRuntime) Health(context.Context) error { return nil }

// Cancel 模拟运行器确认取消指定任务。
func (r *testRuntime) Cancel(context.Context, string, int64) error { return nil }

// Operate 模拟成功、失败和进程中断。
func (r *testRuntime) Operate(context.Context, Operation) (Result, error) {
	r.calls++
	if r.cancel != nil {
		r.cancel()
	}
	if r.fail {
		return Result{ContainerID: "old", ImageID: "old-image", State: "running"}, errors.New("test deployment failed")
	}
	return Result{ContainerID: "new", ImageID: "new-image", State: "running"}, nil
}

// State 模拟安全状态。
func (r *testRuntime) State(context.Context, string, string) (Result, error) {
	return Result{State: "running"}, nil
}

// Logs 模拟含敏感值的应用输出。
func (r *testRuntime) Logs(context.Context, string, string) (string, error) {
	return "value=hidden-test-value", nil
}

// Proxy 模拟应用响应。
func (r *testRuntime) Proxy(http.ResponseWriter, *http.Request, string, string) {}

// TestFailedUpdateRestoresActive 验证失败时恢复旧密文和内存额度，成功才更新活动版本。
func TestFailedUpdateRestoresActive(t *testing.T) {
	secret, id := "thirty-two-byte-test-secret-only", "0c2d84bb-15a1-492b-938a-83282600c112"
	old := testSpec()
	active, _ := seal(secret, id, old)
	next := old
	next.MemoryMiB = 512
	next.Image = "example/app:v2"
	sealed, _ := seal(secret, id, next)
	a := App{ID: id, Action: "deploy", Generation: 2, Sealed: sealed, ActiveSealed: active, MemoryMiB: 512}
	store, runtime := &testStore{value: a}, &testRuntime{fail: true}
	service := New(store, runtime, secret, storage.New(t.TempDir()), nil)
	service.execute(context.Background(), a)
	if store.completed.Sealed != active || store.completed.MemoryMiB != 128 || store.result.ContainerID != "old" || store.message == "" {
		t.Fatal("failed update did not restore", store.completed)
	}
	runtime.fail = false
	service.execute(context.Background(), a)
	if store.completed.ActiveSealed != sealed || store.result.ContainerID != "new" || store.message != "" {
		t.Fatal("successful update not promoted")
	}
	text, err := service.Logs(context.Background(), App{ID: id, Sealed: active})
	if err != nil || strings.Contains(text, "hidden-test-value") {
		t.Fatal("secret leaked in logs", err)
	}
}

// TestCancellationLeavesTaskPending 验证停止后台进程不会把半完成任务错误标为成功。
func TestCancellationLeavesTaskPending(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	id := "0c2d84bb-15a1-492b-938a-83282600c112"
	secret := "thirty-two-byte-test-secret-only"
	sealed, _ := seal(secret, id, testSpec())
	store := &testStore{}
	runtime := &testRuntime{cancel: cancel}
	New(store, runtime, secret, storage.New(t.TempDir()), nil).execute(ctx, App{ID: id, Sealed: sealed, Action: "deploy"})
	if store.completed.ID != "" {
		t.Fatal("cancelled operation completed")
	}
}

// transientStore 模拟数据库恢复队列或提交结果时的一次短暂故障。
type transientStore struct {
	*testStore
	recoverCalls  int
	completeCalls int
	claimCancel   context.CancelFunc
	finishCancel  context.CancelFunc
}

// RecoverApps 首次返回连接故障，再次恢复成功。
func (s *transientStore) RecoverApps(context.Context) error {
	s.recoverCalls++
	if s.recoverCalls == 1 {
		return errors.New("test database temporarily unavailable")
	}
	return nil
}

// ClaimApp 成功恢复后停止测试中的后台任务。
func (s *transientStore) ClaimApp(context.Context) (App, error) {
	if s.claimCancel != nil {
		s.claimCancel()
	}
	return App{}, ErrNotFound
}

// CompleteApp 首次模拟结果保存失败，重试后记录原结果。
func (s *transientStore) CompleteApp(ctx context.Context, a App, result Result, message string) error {
	s.completeCalls++
	if s.completeCalls == 1 {
		if s.finishCancel != nil {
			s.finishCancel()
		}
		return errors.New("test database temporarily unavailable")
	}
	return s.testStore.CompleteApp(ctx, a, result, message)
}

// TestQueueRecoveryRetriesDatabaseFailure 验证数据库短暂中断不会让任务工作进程永久退出。
func TestQueueRecoveryRetriesDatabaseFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := &transientStore{testStore: &testStore{}, claimCancel: cancel}
	New(store, &testRuntime{}, "test-secret", storage.New(t.TempDir()), nil).Run(ctx)
	if store.recoverCalls != 2 || !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("worker did not resume after database recovery: calls=%d context=%v", store.recoverCalls, ctx.Err())
	}
}

// TestCompletionRetriesWithoutRepeatingContainerOperation 验证结果写入失败时只重试保存，不重复容器操作。
func TestCompletionRetriesWithoutRepeatingContainerOperation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failed-operation"}[fail], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			id, secret := "0c2d84bb-15a1-492b-938a-83282600c112", "thirty-two-byte-test-secret-only"
			sealed, err := seal(secret, id, testSpec())
			if err != nil {
				t.Fatal(err)
			}
			store := &transientStore{testStore: &testStore{}}
			runtime := &testRuntime{fail: fail}
			service := New(store, runtime, secret, storage.New(t.TempDir()), nil)
			service.execute(ctx, App{ID: id, Sealed: sealed, Action: "deploy"})
			if store.completeCalls != 2 || store.completed.ID != id || runtime.calls != 1 {
				t.Fatalf("lost or repeated operation: saves=%d completed=%s operations=%d", store.completeCalls, store.completed.ID, runtime.calls)
			}
			if (store.message != "") != fail {
				t.Fatalf("operation result changed during save retry: %q", store.message)
			}
		})
	}
}

// TestCompletionRetryStopsOnShutdown 验证停止服务可以中断结果保存重试。
func TestCompletionRetryStopsOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	id, secret := "0c2d84bb-15a1-492b-938a-83282600c112", "thirty-two-byte-test-secret-only"
	sealed, err := seal(secret, id, testSpec())
	if err != nil {
		t.Fatal(err)
	}
	store := &transientStore{testStore: &testStore{}, finishCancel: cancel}
	runtime := &testRuntime{}
	New(store, runtime, secret, storage.New(t.TempDir()), nil).execute(ctx, App{ID: id, Sealed: sealed, Action: "deploy"})
	if store.completeCalls != 1 || store.completed.ID != "" || runtime.calls != 1 {
		t.Fatal("shutdown did not leave the interrupted task available for startup recovery")
	}
}

// Capacity 为测试提供明确的宿主预算上限。
func (*testRuntime) Capacity(context.Context) (Capacity, error) {
	return Capacity{MaxTotalMemoryMiB: 4096}, nil
}

// Progress 模拟当前代次的有界阶段反馈。
func (*testRuntime) Progress(_ context.Context, _ string, generation int64) (Progress, error) {
	return Progress{Generation: generation, Stage: "pulling"}, nil
}

// AppLimits 模拟保持升级前的实例默认配置。
func (*testStore) AppLimits(context.Context) (LimitsView, error) {
	return LimitsView{Limits: DefaultLimits(), Revision: 1}, nil
}

// UpdateAppLimits 模拟数据库保存预算。
func (*testStore) UpdateAppLimits(_ context.Context, input LimitsUpdate) (LimitsView, error) {
	return LimitsView{Limits: input.Limits, Revision: input.ExpectedRevision + 1}, nil
}

// TestLowerMemoryUpdateReleasesReservation 只在低内存候选成功后释放旧版预留额度，失败恢复仍有空间。
func TestLowerMemoryUpdateReleasesReservation(t *testing.T) {
	secret, id := "thirty-two-byte-test-secret-only", "0c2d84bb-15a1-492b-938a-83282600c112"
	old := testSpec()
	active, err := seal(secret, id, old)
	if err != nil {
		t.Fatal(err)
	}
	next := old
	next.MemoryMiB = 64
	sealed, err := seal(secret, id, next)
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		store := &testStore{}
		service := New(store, &testRuntime{fail: fail}, secret, storage.New(t.TempDir()), nil)
		service.execute(context.Background(), App{ID: id, Action: "deploy", Generation: 2, Sealed: sealed, ActiveSealed: active, MemoryMiB: 128})
		wantMemory, wantSealed := 64, sealed
		if fail {
			wantMemory, wantSealed = 128, active
		}
		if store.completed.MemoryMiB != wantMemory || store.completed.Sealed != wantSealed {
			t.Fatal("incorrect reserved memory after candidate result", fail, store.completed.MemoryMiB)
		}
	}
}
