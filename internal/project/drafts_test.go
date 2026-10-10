package project_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/project"
	"stacknest/server/internal/staticdeploy"
)

// previewContent 验证预览始终从该版本目录读取，不随正式版切换串内容。
func previewContent(t *testing.T, service *project.Service, id, want string) {
	t.Helper()
	root, err := service.OpenPreview(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := root.Open("index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil || string(content) != want {
		t.Fatalf("preview %s: %q, want %q, %v", id, content, want, err)
	}
}

// TestDraftLifecycle 验证草稿隔离、说明、唯一草稿、权限、两版保留及回退后预览不变。
func TestDraftLifecycle(t *testing.T) {
	s, store, _, data := setupProject(t)
	ctx := context.Background()
	owner := project.Actor{ID: store.values["demo"].OwnerID}
	first := project.CurrentID(store.values["demo"])
	current := first
	for _, content := range []string{"second", "third"} {
		input := revisionInput(t, current, content)
		input.Preview, input.Notes = true, "  更新 "+content+"  "
		result, err := s.UpdateAs(ctx, "demo", owner, input)
		if err != nil {
			t.Fatal(err)
		}
		draft := result.Project.Draft
		if draft == nil || draft.Notes != "更新 "+content || project.CurrentID(result.Project) != current {
			t.Fatalf("invalid draft: %+v", result.Project)
		}
		previewContent(t, s, draft.ID, content)
		if _, err := s.UpdateAs(ctx, "demo", owner, input); !errors.Is(err, project.ErrDraftExists) {
			t.Fatalf("replaced draft: %v", err)
		}
		if _, err := s.PublishDraftAs(ctx, "demo", project.Actor{ID: "other"}, draft.ID, current); !errors.Is(err, project.ErrNotFound) {
			t.Fatalf("cross-owner publish: %v", err)
		}
		if _, err := s.PublishDraftAs(ctx, "demo", owner, draft.ID, "stale"); !errors.Is(err, project.ErrVersionConflict) {
			t.Fatalf("stale publish: %v", err)
		}
		files, err := s.FilesAs(ctx, "demo", owner, draft.ID)
		if err != nil || len(files) != 1 {
			t.Fatalf("draft files: %v %v", files, err)
		}
		// 注入事务失败，待发布版仍能预览，正式版恢复原内容。
		store.updateErr = errors.New("database unavailable")
		if _, err := s.PublishDraftAs(ctx, "demo", owner, draft.ID, current); err == nil {
			t.Fatal("expected failure")
		}
		previewContent(t, s, draft.ID, content)
		if project.CurrentID(store.values["demo"]) != current {
			t.Fatal("failure changed published version")
		}
		store.updateErr = nil
		published, err := s.PublishDraftAs(ctx, "demo", owner, draft.ID, current)
		if err != nil {
			t.Fatal(err)
		}
		if published.Project.Draft != nil || len(published.Project.Deployments) != 2 || published.Project.Deployments[0].Notes != draft.Notes {
			t.Fatalf("published metadata: %+v", published.Project)
		}
		current = draft.ID
		previewContent(t, s, current, content)
		if _, err := s.PublishDraftAs(ctx, "demo", owner, current, first); err != nil {
			t.Fatalf("idempotent publish: %v", err)
		}
	}
	if _, err := s.OpenPreview(ctx, first); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("retired version remains accessible: %v", err)
	}
	value, err := s.RollbackAs(ctx, "demo", owner, current)
	if err != nil {
		t.Fatal(err)
	}
	if value.Project.Deployments[1].ID != current {
		t.Fatal("rollback changed version identity")
	}
	previewContent(t, s, current, "third")
	previewContent(t, s, value.Project.Deployments[0].ID, "second")
	assertRevisionFiles(t, data, "second", "third")
	input := revisionInput(t, project.CurrentID(value.Project), "discard")
	input.Preview = true
	result, err := s.UpdateAs(ctx, "demo", owner, input)
	if err != nil {
		t.Fatal(err)
	}
	id := result.Project.Draft.ID
	if _, err := s.DiscardDraftAs(ctx, "demo", owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "drafts", "demo", id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discard retained files: %v", err)
	}
	assertRevisionFiles(t, data, "second", "third")
}

// TestFirstDraftPublish 验证首版未确认前无正式文件，失败恢复与暂停行为一致。
func TestFirstDraftPublish(t *testing.T) {
	data := t.TempDir()
	store := &testStore{values: map[string]project.Project{}}
	d := staticdeploy.New(staticdeploy.Config{DataDir: data, MaxFiles: 10, MaxBytes: 1024})
	s := project.NewService(store, d)
	input := projectInput(t)
	input.Preview, input.Notes = true, "首版说明"
	ctx := context.Background()
	result, err := s.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	value := result.Project
	if value.Status != "draft" || len(value.Deployments) != 0 || value.Draft == nil {
		t.Fatalf("new draft: %+v", value)
	}
	if _, err := os.Stat(filepath.Join(data, "sites", value.Slug)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("draft exposed formal content: %v", err)
	}
	actor := project.Actor{ID: value.OwnerID}
	if _, err := s.SetAccessAs(ctx, value.Slug, actor, false, value.UpdatedAt); !errors.Is(err, project.ErrVersionConflict) {
		t.Fatalf("resumed unpublished project: %v", err)
	}
	previewContent(t, s, value.Draft.ID, "original site")
	store.updateErr = errors.New("database unavailable")
	if _, err := s.PublishDraftAs(ctx, value.Slug, actor, value.Draft.ID, ""); err == nil {
		t.Fatal("expected failure")
	}
	previewContent(t, s, value.Draft.ID, "original site")
	store.updateErr = nil
	published, err := s.PublishDraftAs(ctx, value.Slug, actor, value.Draft.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if published.Project.Status != "online" || len(published.Project.Deployments) != 1 {
		t.Fatal("first publish invalid")
	}
	paused, err := s.SetAccessAs(ctx, value.Slug, actor, true, published.Project.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenPreview(ctx, project.CurrentID(paused)); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("paused preview remains open: %v", err)
	}
}

// TestPublishRetryRepairsAccess 模拟首版提交成功而访问标记遗留，重试会修复标记且不新增版本。
func TestPublishRetryRepairsAccess(t *testing.T) {
	s, store, deployer, root := setupProject(t)
	value := store.values["demo"]
	if err := deployer.SetPaused("demo", true); err != nil {
		t.Fatal(err)
	}
	result, err := s.PublishDraftAs(context.Background(), "demo", project.Actor{ID: value.OwnerID}, project.CurrentID(value), "")
	if err != nil || len(result.Project.Deployments) != 1 {
		t.Fatalf("retry: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "paused", "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("access marker not repaired: %v", err)
	}
}
