package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"stacknest/server/internal/project"
)

func TestStoreIntegration(t *testing.T) {
	databaseURL := os.Getenv("STACKNEST_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("STACKNEST_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	if err := store.BootstrapUsers(ctx, "admin@example.com", ""); err != nil {
		t.Fatalf("bootstrap user: %v", err)
	}
	owner, err := store.FindUserByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	slug := fmt.Sprintf("ent-test-%d", now.UnixNano())
	value := project.Project{
		ID:         fmt.Sprintf("prj-%d", now.UnixNano()),
		OwnerID:    owner.ID,
		OwnerEmail: owner.Email,
		Name:       "Ent Integration",
		Slug:       slug,
		Status:     "online",
		CreatedAt:  now,
		UpdatedAt:  now,
		Deployments: []project.Deployment{
			{
				ID:         fmt.Sprintf("dep-%d", now.UnixNano()),
				Status:     "succeeded",
				FileName:   "site.zip",
				CreatedAt:  now,
				FinishedAt: now,
			},
		},
	}

	if err := store.Create(ctx, value); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Delete(context.Background(), slug, "")
	})

	got, err := store.Get(ctx, slug)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if got.ID != value.ID || got.OwnerID != owner.ID || got.OwnerEmail != owner.Email || len(got.Deployments) != 1 {
		t.Fatalf("unexpected project: %+v", got)
	}
	for i := 1; i <= 3; i++ {
		next := project.Deployment{ID: fmt.Sprintf("%s-revision-%d", value.ID, i), Status: "succeeded", FileName: "updated.zip", CreatedAt: now.Add(time.Duration(i) * time.Second), FinishedAt: now.Add(time.Duration(i) * time.Second)}
		currentID := got.Deployments[0].ID
		if err := store.ReplaceDeployment(ctx, slug, "another-owner", currentID, next); err != project.ErrNotFound {
			t.Fatalf("ownership check failed: %v", err)
		}
		if err := store.ReplaceDeployment(ctx, slug, owner.ID, "stale-id", next); err != project.ErrVersionConflict {
			t.Fatalf("version check failed: %v", err)
		}
		if err := store.ReplaceDeployment(ctx, slug, owner.ID, currentID, next); err != nil {
			t.Fatal(err)
		}
		got, err = store.Get(ctx, slug)
		if err != nil || len(got.Deployments) != 2 || got.Deployments[0].ID != next.ID || got.Deployments[1].ID != currentID || got.ID != value.ID || got.OwnerID != owner.ID || got.Name != value.Name {
			t.Fatalf("revision transaction inconsistent: %+v %v", got, err)
		}
	}

	if err := store.Delete(ctx, slug, ""); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if _, err := store.Get(ctx, slug); err != project.ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
