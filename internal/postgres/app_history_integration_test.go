package postgres

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/containerapp"
)

// TestAppHistoryIntegration 验证队列、恢复、重复完成、取消、十条保留及删除的事务一致性。
func TestAppHistoryIntegration(t *testing.T) {
	databaseURL := os.Getenv("STACKNEST_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("dedicated database required")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Path != "/stacknest_test" {
		t.Fatal("dedicated database required")
	}
	ctx := context.Background()
	s, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.BootstrapUsers(ctx, "admin@example.com", ""); err != nil {
		t.Fatal(err)
	}
	u, err := s.FindUserByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	actor := containerapp.Actor{ID: u.ID, Email: u.Email}
	id := uuid.NewString()
	a := containerapp.App{ID: id, Name: "history", Slug: "history-" + id[:8], Sealed: "fixture", MemoryMiB: 64, Image: "example/app:v0"}
	defer s.db.Exec("DELETE FROM container_apps WHERE id=$1", id)
	for i := 0; i < 12; i++ {
		a.Image = fmt.Sprintf("example/app:v%d", i)
		if err = s.SaveApp(ctx, a, actor, i == 0); err != nil {
			t.Fatal(err)
		}
		a, err = s.ClaimApp(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecoverApps(ctx); err != nil {
			t.Fatal(err)
		}
		a, err = s.ClaimApp(ctx)
		if err != nil {
			t.Fatal(err)
		}
		message := ""
		if i == 10 {
			message = "startup failed [已隐藏]"
		}
		result := containerapp.Result{State: "running", ImageID: "sha256:fixture"}
		if err = s.CompleteApp(ctx, a, result, message); err != nil {
			t.Fatal(err)
		}
		items, err := s.AppDeployments(ctx, id)
		if err != nil || len(items) != min(i+1, 10) || items[0].StartedAt == nil || items[0].FinishedAt == nil {
			t.Fatal(items, err)
		}
		before := *items[0].FinishedAt
		if err = s.CompleteApp(ctx, a, result, message); err != nil {
			t.Fatal(err)
		}
		items, err = s.AppDeployments(ctx, id)
		if err != nil || !before.Equal(*items[0].FinishedAt) {
			t.Fatal("duplicate completion changed record", err)
		}
		if i == 10 && (items[0].Status != "failed" || items[0].ImageID != "" || items[0].Image != "example/app:v10") {
			t.Fatal(items[0])
		}
		a, err = s.App(ctx, id, actor)
		if err != nil {
			t.Fatal(err)
		}
	}
	a.Image = "example/app:canceled"
	if err = s.SaveApp(ctx, a, actor, false); err != nil {
		t.Fatal(err)
	}
	a, err = s.App(ctx, id, actor)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CancelPendingApp(ctx, a); err != nil {
		t.Fatal(err)
	}
	items, err := s.AppDeployments(ctx, id)
	if err != nil || len(items) != 10 || items[0].Status != "canceled" || items[0].StartedAt != nil {
		t.Fatal(items, err)
	}
	if items[0].FinishedAt.After(time.Now().Add(time.Minute)) {
		t.Fatal("invalid completion time")
	}
	a.Action = "delete"
	if err = s.CompleteApp(ctx, a, containerapp.Result{}, ""); err != nil {
		t.Fatal(err)
	}
	items, err = s.AppDeployments(ctx, id)
	if err != nil || len(items) != 0 {
		t.Fatal("history not deleted with app", items, err)
	}
}
