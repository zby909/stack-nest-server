package containerapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"stacknest/server/internal/storage"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type timedRuntime struct {
	testRuntime
	delay time.Duration
}

// Operate 模拟超过旧十分钟限制的下载和无响应运行器。
func (r *timedRuntime) Operate(ctx context.Context, _ Operation) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-time.After(r.delay):
		return Result{ContainerID: "new", ImageID: "new-image", State: "running"}, nil
	}
}

// TestWorkerAllowsSlowPullAndBoundsOperations 穿过真实工作进程，防止外层仍提前取消慢速下载。
func TestWorkerAllowsSlowPullAndBoundsOperations(t *testing.T) {
	for _, tc := range []struct {
		action  string
		delay   time.Duration
		timeout bool
	}{
		{"deploy", 20 * time.Minute, false}, {"start", 20 * time.Minute, false}, {"restart", 20 * time.Minute, false},
		{"deploy", 2 * time.Hour, true}, {"stop", 20 * time.Minute, true}, {"delete", 20 * time.Minute, true},
	} {
		t.Run(tc.action+tc.delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				id, secret := "0c2d84bb-15a1-492b-938a-83282600c112", "thirty-two-byte-test-secret-only"
				sealed, err := seal(secret, id, testSpec())
				if err != nil {
					t.Fatal(err)
				}
				store := &testStore{}
				service := New(store, &timedRuntime{delay: tc.delay}, secret, storage.New(t.TempDir()), nil)
				service.execute(context.Background(), App{ID: id, Sealed: sealed, Action: tc.action})
				if store.completed.ID != id {
					t.Fatal("result was not persisted")
				}
				if tc.timeout {
					if store.message != ErrOperationTimeout.Error() {
						t.Fatal(store.message)
					}
				} else if store.message != "" || store.result.ContainerID != "new" {
					t.Fatalf("active download canceled: %q", store.message)
				}
			})
		})
	}
}

type timeoutTransport func(*http.Request) (*http.Response, error)

// RoundTrip 在内存中模拟 Unix socket 的响应和断开。
func (f timeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestClientPreservesTimeoutReason 区分任务截止、运行器返回的拉取错误和真正的连接失败。
func TestClientPreservesTimeoutReason(t *testing.T) {
	for _, reason := range []string{"deadline", "cancel", "unavailable", "runner-error"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				client := &Client{client: &http.Client{Transport: timeoutTransport(func(req *http.Request) (*http.Response, error) {
					if reason == "runner-error" {
						return &http.Response{StatusCode: 409, Body: io.NopCloser(strings.NewReader(`{"error":"镜像拉取超时：没有进展","result":{"state":"running","containerId":"old"}}`))}, nil
					}
					if reason == "unavailable" {
						return nil, errors.New("private connection detail")
					}
					if reason == "cancel" {
						cancel()
					}
					<-req.Context().Done()
					return nil, req.Context().Err()
				})}}
				value, err := client.Operate(ctx, Operation{})
				if reason == "runner-error" {
					if err == nil || err.Error() != "镜像拉取超时：没有进展" || value.ContainerID != "old" {
						t.Fatalf("lost runner failure: %+v %v", value, err)
					}
					return
				}
				want := map[string]error{"deadline": ErrOperationTimeout, "cancel": context.Canceled, "unavailable": ErrUnavailable}[reason]
				if !errors.Is(err, want) {
					t.Fatalf("want %v got %v", want, err)
				}
			})
		})
	}
}
