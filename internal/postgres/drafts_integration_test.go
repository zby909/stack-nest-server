package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/project"
)

// TestDraftStoreIntegration 验证真实数据库中草稿分离、更新说明、首次发布与两版保留。
func TestDraftStoreIntegration(t *testing.T) {
	url := os.Getenv("STACKNEST_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("STACKNEST_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.BootstrapUsers(ctx, "admin@example.com", ""); err != nil {
		t.Fatal(err)
	}
	owner, err := s.FindUserByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	draft := project.Deployment{ID: "dep-" + uuid.NewString(), Status: "draft", FileName: "first.zip", Notes: "首版\n中文说明", CreatedAt: now, FinishedAt: now}
	value := project.Project{ID: "prj-" + uuid.NewString(), Slug: "draft-" + uuid.NewString(), Name: "Draft", OwnerID: owner.ID, Status: "draft", CreatedAt: now, UpdatedAt: now, Draft: &draft}
	if err := s.Create(ctx, value); err != nil {
		t.Fatal(err)
	}
	defer s.Delete(ctx, value.Slug, owner.ID)
	got, err := s.Get(ctx, value.Slug)
	if err != nil || got.Draft == nil || got.Draft.Notes != draft.Notes || len(got.Deployments) != 0 {
		t.Fatalf("draft roundtrip: %+v %v", got, err)
	}
	firstID := draft.ID
	for i := 0; i < 3; i++ {
		current := project.CurrentID(got)
		if i > 0 {
			draft.ID, draft.CreatedAt, draft.FinishedAt = "dep-"+uuid.NewString(), now.Add(time.Duration(i)*time.Second), now.Add(time.Duration(i)*time.Second)
			if err := s.SaveDraft(ctx, value.Slug, owner.ID, "stale", "", draft); !errors.Is(err, project.ErrVersionConflict) {
				t.Fatalf("stale draft accepted: %v", err)
			}
			if err := s.SaveDraft(ctx, value.Slug, owner.ID, current, "", draft); err != nil {
				t.Fatal(err)
			}
			if err := s.SaveDraft(ctx, value.Slug, owner.ID, current, "", draft); !errors.Is(err, project.ErrDraftExists) {
				t.Fatalf("duplicate draft accepted: %v", err)
			}
			got, err = s.Get(ctx, value.Slug)
			if err != nil || project.CurrentID(got) != current || got.Draft.ID != draft.ID {
				t.Fatalf("draft replaced formal: %+v %v", got, err)
			}
		}
		next := draft
		next.Status, next.CreatedAt, next.FinishedAt = "succeeded", draft.CreatedAt.Add(time.Millisecond), draft.CreatedAt.Add(time.Millisecond)
		if err := s.ReplaceDeployment(ctx, value.Slug, owner.ID, current, next); err != nil {
			t.Fatal(err)
		}
		got, err = s.Get(ctx, value.Slug)
		if err != nil || got.Status != "online" || got.Draft != nil || len(got.Deployments) != min(i+1, 2) || got.Deployments[0].Notes != draft.Notes {
			t.Fatalf("publish: %+v %v", got, err)
		}
	}
	for _, item := range got.Deployments {
		if item.ID == firstID {
			t.Fatal("third published version retained")
		}
	}
	old := got.Deployments[1]
	old.CreatedAt, old.FinishedAt = got.Deployments[0].CreatedAt.Add(time.Second), got.Deployments[0].CreatedAt.Add(time.Second)
	if err := s.ReplaceDeployment(ctx, value.Slug, owner.ID, got.Deployments[0].ID, old); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, value.Slug)
	if err != nil || got.Deployments[0].ID != old.ID || got.Deployments[0].Notes != old.Notes {
		t.Fatalf("rollback identity: %+v %v", got, err)
	}
	draft.ID, draft.CreatedAt = "dep-"+uuid.NewString(), old.CreatedAt.Add(time.Second)
	if err := s.SaveDraft(ctx, value.Slug, owner.ID, old.ID, "", draft); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDraft(ctx, value.Slug, "other", draft.ID); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("cross-owner discard: %v", err)
	}
	if err := s.DeleteDraft(ctx, value.Slug, owner.ID, draft.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.Get(ctx, value.Slug)
	if err != nil || got.Draft != nil || len(got.Deployments) != 2 {
		t.Fatalf("discard changed formal: %+v %v", got, err)
	}
}
