package containerapp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

type imageRuntimeFixture struct {
	testRuntime
	request ImageRequest
	deleted int
}

// Images 记录服务层传给运行器的保护引用。
func (r *imageRuntimeFixture) Images(_ context.Context, request ImageRequest) (ImageInventory, error) {
	r.request = request
	return ImageInventory{Images: []ImageUsage{}}, nil
}

// DeleteImage 只记录真正通过服务层互斥与状态检查的删除。
func (r *imageRuntimeFixture) DeleteImage(_ context.Context, request ImageRequest) error {
	r.request = request
	r.deleted++
	return nil
}

// TestImageReferencesAndQueueLock 保护停止应用、恢复配置和排队任务，拒绝沿用旧清单。
func TestImageReferencesAndQueueLock(t *testing.T) {
	ctx := context.Background()
	const secret = "image-cleanup-test-secret"
	const id = "0c2d84bb-15a1-492b-938a-83282600c112"
	spec := testSpec()
	sealed, err := seal(secret, id, spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Image = "docker.io/example/app:previous"
	previous, err := seal(secret, id, spec)
	if err != nil {
		t.Fatal(err)
	}
	imageID := "sha256:" + strings.Repeat("a", 64)
	store := &testStore{value: App{ID: id, Status: "stopped", ImageID: imageID, Sealed: sealed, ActiveSealed: previous}}
	runtime := &imageRuntimeFixture{}
	service := New(store, runtime, secret, nil, nil)
	if _, err := service.Images(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{imageID, testSpec().Image, spec.Image} {
		if !slices.Contains(runtime.request.Protected, ref) {
			t.Fatal("missing protected reference", ref)
		}
	}
	for _, status := range []string{"pending", "deploying"} {
		store.value.Status = status
		if err := service.DeleteImage(ctx, imageID); !errors.Is(err, ErrBusy) {
			t.Fatal(status, err)
		}
	}
	store.value.Status = "stopped"
	service.imageMu.RLock()
	err = service.DeleteImage(ctx, imageID)
	service.imageMu.RUnlock()
	if !errors.Is(err, ErrBusy) {
		t.Fatal("enqueue lock bypassed", err)
	}
	store.value.Sealed = "corrupt"
	if err := service.DeleteImage(ctx, imageID); err == nil {
		t.Fatal("corrupt config allowed deletion")
	}
	store.value.Sealed = sealed
	if err := service.DeleteImage(ctx, "example/app:latest"); err == nil {
		t.Fatal("mutable reference allowed")
	}
	if runtime.deleted != 0 {
		t.Fatal("unsafe deletion reached runner")
	}
	if err := service.DeleteImage(ctx, imageID); err != nil {
		t.Fatal(err)
	}
	if runtime.deleted != 1 || runtime.request.ID != imageID {
		t.Fatal("safe request not forwarded")
	}
}
