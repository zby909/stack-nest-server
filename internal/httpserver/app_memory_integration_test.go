package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/postgres"
	"stacknest/server/internal/storage"
	"testing"

	"github.com/google/uuid"
)

// TestAppMemoryBudgetIntegration 验证真实数据库接受两个 1 GiB 应用，并拒绝总预算和单应用超额。
func TestAppMemoryBudgetIntegration(t *testing.T) {
	databaseURL := os.Getenv("STACKNEST_TEST_AUTH_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("dedicated auth database required")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Path != "/stacknest_auth_test" {
		t.Fatal("dedicated auth database required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("TRUNCATE auth_users CASCADE"); err != nil {
		t.Fatal(err)
	}
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	limits, err := store.AppLimits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ownerID := uuid.NewString()
	defer func() {
		if _, err := db.Exec("DELETE FROM container_apps WHERE owner_user_id=$1", ownerID); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec("DELETE FROM auth_users WHERE id=$1", ownerID); err != nil {
			t.Error(err)
		}
		if _, err := db.Exec("UPDATE app_limits SET max_apps=$1,total_memory_mib=$2,revision=$3 WHERE id=1", limits.MaxApps, limits.TotalMemoryMiB, limits.Revision); err != nil {
			t.Error(err)
		}
	}()
	if _, err = db.Exec("INSERT INTO auth_users(id,email,role,created_at) VALUES($1,'memory-budget@example.com','user',now())", ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpdateAppLimits(ctx, containerapp.LimitsUpdate{Limits: containerapp.Limits{MaxApps: 3, TotalMemoryMiB: 2048}, ExpectedRevision: limits.Revision}); err != nil {
		t.Fatal(err)
	}
	apps := containerapp.New(store, appTestRuntime{}, "test-only-secret-with-at-least-thirty-two-bytes", storage.New(t.TempDir()), nil)
	actor := containerapp.Actor{ID: ownerID, Email: "memory-budget@example.com"}
	for _, slug := range []string{"memory-one", "memory-two"} {
		input := containerapp.Input{Name: slug, Slug: slug, Spec: containerapp.Spec{Image: "example/http:v1", Port: 3000, MemoryMiB: 1024, CPUs: 0.5}}
		if _, err = apps.Save(ctx, "", actor, input, 0); err != nil {
			t.Fatal("1 GiB application rejected", err)
		}
	}
	saved, err := store.AppLimits(ctx)
	if err != nil || saved.UsedApps != 2 || saved.AllocatedMemoryMiB != 2048 {
		t.Fatal("incorrect 2 GiB memory reservation", saved, err)
	}
	input := containerapp.Input{Name: "excess", Slug: "memory-excess", Spec: containerapp.Spec{Image: "example/http:v1", Port: 3000, MemoryMiB: 64, CPUs: 0.5}}
	if _, err = apps.Save(ctx, "", actor, input, 0); !errors.Is(err, containerapp.ErrQuota) {
		t.Fatal("total memory budget overcommitted", err)
	}
	input.MemoryMiB = 1025
	if _, err = apps.Save(ctx, "", actor, input, 0); !errors.Is(err, containerapp.ErrInvalid) {
		t.Fatal("single application memory limit exceeded", err)
	}
}
