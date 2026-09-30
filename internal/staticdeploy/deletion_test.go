package staticdeploy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/project"
)

// pendingDeletion 创建一个已暂存但尚未完成数据库删除的站点。
func pendingDeletion(t *testing.T) (*Deployer, project.Deletion, string) {
	t.Helper()
	root := t.TempDir()
	deployer := New(Config{DataDir: root, MaxFiles: 10, MaxBytes: 1024})
	if _, err := deployer.Publish(context.Background(), "demo", "site.zip", bytes.NewReader(testZip(t))); err != nil {
		t.Fatal(err)
	}
	if err := deployer.CompletePublication("demo"); err != nil {
		t.Fatal(err)
	}
	deletion, err := deployer.StageDelete(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	return deployer, deletion, root
}

// TestRecoverInterruptedDeletion 验证中断删除按数据库状态恢复或完成清理。
func TestRecoverInterruptedDeletion(t *testing.T) {
	for _, exists := range []bool{true, false} {
		name := "after metadata deletion"
		if exists {
			name = "before metadata deletion"
		}
		t.Run(name, func(t *testing.T) {
			_, _, root := pendingDeletion(t)
			restarted := New(Config{DataDir: root})
			lookup := func(_ context.Context, slug string) (project.Project, error) {
				if slug != "demo" {
					t.Fatalf("unexpected slug: %s", slug)
				}
				if exists {
					return project.Project{Slug: slug}, nil
				}
				return project.Project{}, project.ErrNotFound
			}
			if err := restarted.RecoverDeletions(context.Background(), lookup); err != nil {
				t.Fatal(err)
			}
			_, siteErr := os.Stat(filepath.Join(root, "sites", "demo", "index.html"))
			if (exists && siteErr != nil) || (!exists && !errors.Is(siteErr, os.ErrNotExist)) {
				t.Fatalf("unexpected recovered site: %v", siteErr)
			}
			if _, err := os.Stat(filepath.Join(root, "trash", "demo")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("pending files remain: %v", err)
			}
		})
	}
}

// TestRecoveryLookupFailurePreservesFiles 验证数据库不可用时不会猜测或清理暂存内容。
func TestRecoveryLookupFailurePreservesFiles(t *testing.T) {
	deployer, _, root := pendingDeletion(t)
	cause := errors.New("database unavailable")
	err := deployer.RecoverDeletions(context.Background(), func(context.Context, string) (project.Project, error) {
		return project.Project{}, cause
	})
	if !errors.Is(err, cause) {
		t.Fatalf("expected lookup failure, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "trash", "demo", "index.html")); err != nil {
		t.Fatalf("recovery lost staged files: %v", err)
	}
}

// TestRollbackNeverOverwritesExistingSite 验证恢复时不会覆盖已经存在的文件。
func TestRollbackNeverOverwritesExistingSite(t *testing.T) {
	_, deletion, root := pendingDeletion(t)
	site := filepath.Join(root, "sites", "demo")
	if err := os.Mkdir(site, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("new site"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := deletion.Rollback(); err == nil {
		t.Fatal("expected restore conflict")
	}
	content, err := os.ReadFile(filepath.Join(site, "index.html"))
	if err != nil || string(content) != "new site" {
		t.Fatalf("existing site changed: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "trash", "demo", "index.html")); err != nil {
		t.Fatalf("original site was lost: %v", err)
	}
}

// TestStageDeleteMissingSite 验证历史缺失文件的项目仍可完成元数据清理。
func TestStageDeleteMissingSite(t *testing.T) {
	deployer := New(Config{DataDir: t.TempDir()})
	deletion, err := deployer.StageDelete(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if err := deletion.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := deletion.Commit(); err != nil {
		t.Fatal(err)
	}
}
