package project_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"stacknest/server/internal/project"
	"stacknest/server/internal/staticdeploy"
)

type testStore struct {
	values       map[string]project.Project
	createErr    error
	deleteErr    error
	deleteCalls  int
	beforeDelete func()
	updateErr    error
	beforeUpdate func()
}

// UpdateMetadata 模拟归属与过期版本保护，同时支持存储故障注入。
func (s *testStore) UpdateMetadata(ctx context.Context, value project.Project, expectedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.updateErr != nil {
		return s.updateErr
	}
	old, ok := s.values[value.Slug]
	if !ok || old.OwnerID != value.OwnerID || !old.UpdatedAt.Equal(expectedAt) {
		return project.ErrVersionConflict
	}
	s.values[value.Slug] = value
	return nil
}

// ReplaceDeployment 模拟事务中的权限、版本检查以及故障注入。
func (s *testStore) ReplaceDeployment(ctx context.Context, slug, ownerID, expectedID string, next project.Deployment) error {
	if s.beforeUpdate != nil {
		s.beforeUpdate()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.updateErr != nil {
		return s.updateErr
	}
	value, ok := s.values[slug]
	if !ok || ownerID != "" && value.OwnerID != ownerID {
		return project.ErrNotFound
	}
	if project.CurrentID(value) != expectedID {
		return project.ErrVersionConflict
	}
	if len(value.Deployments) > 0 {
		value.Deployments = []project.Deployment{next, value.Deployments[0]}
	} else {
		value.Deployments = []project.Deployment{next}
	}
	if value.Draft != nil && value.Draft.ID == next.ID {
		value.Draft = nil
	}
	if value.Status == "draft" {
		value.Status = "online"
	}
	value.UpdatedAt = next.CreatedAt
	s.values[slug] = value
	return nil
}

// List 返回测试中已持久化的项目。
func (s *testStore) List(context.Context) ([]project.Project, error) {
	values := make([]project.Project, 0, len(s.values))
	for _, value := range s.values {
		values = append(values, value)
	}
	return values, nil
}

// Get 模拟按 slug 查询项目。
func (s *testStore) Get(_ context.Context, slug string) (project.Project, error) {
	value, ok := s.values[slug]
	if !ok {
		return project.Project{}, project.ErrNotFound
	}
	return value, nil
}

// Create 模拟正常写入以及数据库写入故障。
func (s *testStore) Create(_ context.Context, value project.Project) error {
	if s.createErr != nil {
		return s.createErr
	}
	if _, exists := s.values[value.Slug]; exists {
		return project.ErrAlreadyExists
	}
	s.values[value.Slug] = value
	return nil
}

// Delete 支持注入数据库失败和请求取消。
func (s *testStore) Delete(ctx context.Context, slug, ownerID string) error {
	s.deleteCalls++
	if s.beforeDelete != nil {
		s.beforeDelete()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if ownerID != "" && s.values[slug].OwnerID != ownerID {
		return project.ErrNotFound
	}
	delete(s.values, slug)
	return nil
}

// setupProject 创建真实静态文件与可注入故障的元数据存储。
func setupProject(t *testing.T) (*project.Service, *testStore, *staticdeploy.Deployer, string) {
	t.Helper()
	root := t.TempDir()
	store := &testStore{values: make(map[string]project.Project)}
	deployer := staticdeploy.New(staticdeploy.Config{DataDir: root, MaxFiles: 10, MaxBytes: 1024})
	service := project.NewService(store, deployer)
	if _, err := service.Create(context.Background(), projectInput(t)); err != nil {
		t.Fatal(err)
	}
	return service, store, deployer, root
}

// projectInput 构造用于验证文件保留与清理的有效 ZIP。
func projectInput(t *testing.T) project.CreateInput {
	t.Helper()
	var source bytes.Buffer
	writer := zip.NewWriter(&source)
	file, err := writer.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("original site")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return project.CreateInput{OwnerID: "test-owner", OwnerEmail: "test@example.com", Name: "Demo", Slug: "demo", FileName: "site.zip", Source: &source}
}

// assertSiteContent 验证失败恢复后原站点内容未发生变化。
func assertSiteContent(t *testing.T, root string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, "sites", "demo", "index.html"))
	if err != nil || string(content) != "original site" {
		t.Fatalf("expected intact original site, got %q, error: %v", content, err)
	}
}

// TestDeleteFailureRestoresSite 验证数据库失败和取消请求都会恢复站点。
func TestDeleteFailureRestoresSite(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "database failure"
		if canceled {
			name = "canceled request"
		}
		t.Run(name, func(t *testing.T) {
			service, store, _, root := setupProject(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("database unavailable")
			store.deleteErr = cause
			if canceled {
				store.beforeDelete = cancel
				cause = context.Canceled
			}
			if err := service.DeleteAs(ctx, "demo", project.Actor{IsAdmin: true}); !errors.Is(err, cause) {
				t.Fatalf("expected %v, got %v", cause, err)
			}
			value, err := store.Get(context.Background(), "demo")
			if err != nil || value.Status != "online" {
				t.Fatalf("project metadata changed: %+v, %v", value, err)
			}
			assertSiteContent(t, root)
			if _, err := os.Stat(filepath.Join(root, "trash", "demo")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rollback left pending files: %v", err)
			}
		})
	}
}

// TestDeleteStageFailureKeepsProject 验证无法暂存文件时不会删除数据库记录。
func TestDeleteStageFailureKeepsProject(t *testing.T) {
	service, store, _, root := setupProject(t)
	if err := os.WriteFile(filepath.Join(root, "trash"), []byte("blocked"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAs(context.Background(), "demo", project.Actor{IsAdmin: true}); err == nil {
		t.Fatal("expected staging error")
	}
	if store.deleteCalls != 0 {
		t.Fatal("metadata deletion started before files were staged")
	}
	assertSiteContent(t, root)
}

// TestDeleteAllowsReusingSlug 验证成功删除后记录、文件与暂存均清理完毕。
func TestDeleteAllowsReusingSlug(t *testing.T) {
	service, store, _, root := setupProject(t)
	if err := service.DeleteAs(context.Background(), "demo", project.Actor{IsAdmin: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "demo"); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("project still exists: %v", err)
	}
	for _, directory := range []string{"sites", "trash"} {
		if _, err := os.Stat(filepath.Join(root, directory, "demo")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists: %v", directory, err)
		}
	}
	if _, err := service.Create(context.Background(), projectInput(t)); err != nil {
		t.Fatalf("cannot reuse deleted slug: %v", err)
	}
}

// TestConcurrentDeletes 验证重复并发删除只有一次实际删除操作。
func TestConcurrentDeletes(t *testing.T) {
	service, store, _, _ := setupProject(t)
	var wait sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- service.DeleteAs(context.Background(), "demo", project.Actor{IsAdmin: true})
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	var succeeded, missing int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, project.ErrNotFound):
			missing++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || missing != 1 || store.deleteCalls != 1 {
		t.Fatalf("unexpected delete results: success=%d missing=%d calls=%d", succeeded, missing, store.deleteCalls)
	}
}

// TestDeleteAsRequiresOwnership 验证越权删除不会触碰文件，归属转移并发时会恢复暂存文件。
func TestDeleteAsRequiresOwnership(t *testing.T) {
	service, store, _, root := setupProject(t)
	if err := service.DeleteAs(context.Background(), "demo", project.Actor{ID: "other-user"}); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("cross-user delete returned %v", err)
	}
	if store.deleteCalls != 0 {
		t.Fatal("unauthorized delete reached store")
	}
	assertSiteContent(t, root)
	store.beforeDelete = func() {
		value := store.values["demo"]
		value.OwnerID = "new-owner"
		store.values["demo"] = value
	}
	if err := service.DeleteAs(context.Background(), "demo", project.Actor{ID: "test-owner"}); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("delete after ownership transfer returned %v", err)
	}
	assertSiteContent(t, root)
	if got := store.values["demo"].OwnerID; got != "new-owner" {
		t.Fatalf("owner changed unexpectedly: %s", got)
	}
}

// TestCreateFailureCleansFiles 验证新增项目写库失败仍会清理已发布文件。
func TestCreateFailureCleansFiles(t *testing.T) {
	service, store, _, root := setupProject(t)
	if err := service.DeleteAs(context.Background(), "demo", project.Actor{IsAdmin: true}); err != nil {
		t.Fatal(err)
	}
	store.createErr = errors.New("database unavailable")
	if _, err := service.Create(context.Background(), projectInput(t)); !errors.Is(err, store.createErr) {
		t.Fatalf("expected create failure, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sites", "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed create left a published site: %v", err)
	}
}

type cleanupFailurePublisher struct{ *staticdeploy.Deployer }
type cleanupFailureDeletion struct{ project.Deletion }

// StageDelete 仅替换最后的清理步骤，以复现磁盘清理失败。
func (p cleanupFailurePublisher) StageDelete(ctx context.Context, slug string) (project.Deletion, error) {
	deletion, err := p.Deployer.StageDelete(ctx, slug)
	return cleanupFailureDeletion{deletion}, err
}

// Commit 模拟不可访问目录导致的清理失败。
func (cleanupFailureDeletion) Commit() error {
	return errors.New("cleanup unavailable")
}

// TestCleanupFailureRecoversOnRestart 验证清理失败不重新公开项目且启动后可完成清理。
func TestCleanupFailureRecoversOnRestart(t *testing.T) {
	_, store, deployer, root := setupProject(t)
	service := project.NewService(store, cleanupFailurePublisher{deployer})
	if err := service.DeleteAs(context.Background(), "demo", project.Actor{IsAdmin: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sites", "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted site remains public: %v", err)
	}
	if _, err := service.Create(context.Background(), projectInput(t)); !errors.Is(err, project.ErrAlreadyExists) {
		t.Fatalf("pending deletion allowed slug reuse: %v", err)
	}
	if err := deployer.RecoverDeletions(context.Background(), store.Get); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), projectInput(t)); err != nil {
		t.Fatalf("cleanup did not release slug: %v", err)
	}
}

// SaveDraft 模拟待发布版本事务。
func (s *testStore) SaveDraft(ctx context.Context, slug, ownerID, expectedID string, next project.Deployment) error {
	value, err := s.Get(ctx, slug)
	if err != nil {
		return err
	}
	if value.OwnerID != ownerID {
		return project.ErrNotFound
	}
	if project.CurrentID(value) != expectedID {
		return project.ErrVersionConflict
	}
	if value.Draft != nil {
		return project.ErrDraftExists
	}
	value.Draft, value.UpdatedAt = &next, next.CreatedAt
	s.values[slug] = value
	return nil
}

// DeleteDraft 模拟按草稿编号删除，正式版不会被误删。
func (s *testStore) DeleteDraft(ctx context.Context, slug, ownerID, draftID string) error {
	value, err := s.Get(ctx, slug)
	if err != nil {
		return err
	}
	if value.OwnerID != ownerID {
		return project.ErrNotFound
	}
	if value.Draft == nil || value.Draft.ID != draftID {
		return project.ErrVersionConflict
	}
	value.Draft = nil
	s.values[slug] = value
	return nil
}
