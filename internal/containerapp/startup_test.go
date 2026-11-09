package containerapp

import (
	"context"
	"testing"
	"time"

	"stacknest/server/internal/storage"
)

// TestStartupTimeoutSettings 验证默认值、旧客户端保留配置及后端边界，不能仅依赖表单限制。
func TestStartupTimeoutSettings(t *testing.T) {
	for _, seconds := range []int{-1, 1, 29, 301, 1000000} {
		spec := testSpec()
		spec.StartupTimeoutSeconds = seconds
		if spec.Validate() == nil {
			t.Fatal("invalid timeout accepted", seconds)
		}
	}
	guard := storage.New(t.TempDir())
	guard.Read = func(string) (storage.Usage, error) {
		return storage.Usage{TotalBytes: 40 << 30, FreeBytes: 30 << 30}, nil
	}
	store := &testStore{}
	service := New(store, &testRuntime{}, "thirty-two-byte-test-secret-only", guard, nil)
	input := Input{Name: "App", Slug: "app", Spec: testSpec()}
	a, err := service.Save(context.Background(), "", Actor{}, input, 0)
	if err != nil || a.StartupTimeoutSeconds != 60 {
		t.Fatal("default startup timeout", a.StartupTimeoutSeconds, err)
	}
	for _, seconds := range []int{30, 180, 0, 300, 60} {
		input.StartupTimeoutSeconds = seconds
		before := a.StartupTimeoutSeconds
		a, err = service.Save(context.Background(), a.ID, Actor{}, input, a.Generation)
		want := seconds
		if want == 0 {
			want = before
		}
		if err != nil || a.StartupTimeoutSeconds != want {
			t.Fatal("saved timeout", a.StartupTimeoutSeconds, want, err)
		}
		stored, err := open(service.secret, a.ID, store.value.Sealed)
		if err != nil || stored.StartupTimeout() != time.Duration(want)*time.Second {
			t.Fatal("encrypted timeout did not survive", stored, err)
		}
	}
}
