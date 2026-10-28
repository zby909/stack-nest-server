package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/project"
)

// TestReplaceDraftStoreIntegration 用真实事务验证替换失败回滚和并发更新只成功一个。
func TestReplaceDraftStoreIntegration(t *testing.T) {
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
	old := project.Deployment{ID: "dep-" + uuid.NewString(), Status: "draft", FileName: "old.html", CreatedAt: now, FinishedAt: now}
	value := project.Project{ID: "prj-" + uuid.NewString(), Slug: "replace-" + uuid.NewString(), OwnerID: owner.ID, Name: "Replacement", Status: "draft", CreatedAt: now, UpdatedAt: now, Draft: &old}
	if err := s.Create(ctx, value); err != nil {
		t.Fatal(err)
	}
	defer s.Delete(ctx, value.Slug, owner.ID)
	// 主键碰撞发生在删除旧行之后，事务回滚必须恢复旧草稿。
	collision := old
	collision.ID = "dep-" + uuid.NewString()
	other := value
	other.ID, other.Slug, other.Draft = "prj-"+uuid.NewString(), "collision-"+uuid.NewString(), &collision
	if err := s.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	defer s.Delete(ctx, other.Slug, owner.ID)
	if err := s.SaveDraft(ctx, value.Slug, owner.ID, "", old.ID, collision); err == nil {
		t.Fatal("expected duplicate key rejection")
	}
	stored, err := s.Get(ctx, value.Slug)
	if err != nil || stored.Draft == nil || stored.Draft.ID != old.ID {
		t.Fatal("failed transaction lost old draft", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			next := old
			next.ID, next.CreatedAt = "dep-"+uuid.NewString(), now.Add(time.Second)
			<-start
			results <- s.SaveDraft(ctx, value.Slug, owner.ID, "", old.ID, next)
		}()
	}
	close(start)
	group.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, project.ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal("concurrent replacement lost updates", wins, conflicts)
	}
	stored, err = s.Get(ctx, value.Slug)
	if err != nil || stored.Draft == nil || stored.Draft.ID == old.ID || len(stored.Deployments) != 0 || stored.Status != "draft" {
		t.Fatal("replacement changed first-publication status", err)
	}
}
