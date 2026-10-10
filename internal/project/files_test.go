package project_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/project"
)

// TestFilesRespectVersionsAndOwnership 验证目录与版本一致，管理员可查看、其他普通用户不可见。
func TestFilesRespectVersionsAndOwnership(t *testing.T) {
	s, store, _, root := setupProject(t)
	ctx := context.Background()
	owner := project.Actor{ID: "test-owner"}
	oldID := store.values["demo"].Deployments[0].ID
	updated, err := s.UpdateAs(ctx, "demo", owner, revisionInput(t, oldID, "second"))
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []project.Actor{owner, {ID: "admin", IsAdmin: true}} {
		for id, size := range map[string]int64{oldID: 13, updated.Project.Deployments[0].ID: 6} {
			files, err := s.FilesAs(ctx, "demo", actor, id)
			if err != nil || len(files) != 1 || files[0].Path != "index.html" || files[0].Bytes != size {
				t.Fatalf("version list: %+v %v", files, err)
			}
		}
	}
	if _, err := s.FilesAs(ctx, "demo", project.Actor{ID: "foreign"}, oldID); !errors.Is(err, project.ErrNotFound) {
		t.Fatal("cross user list", err)
	}
	if _, err := s.FilesAs(ctx, "demo", owner, "not-a-version"); !errors.Is(err, project.ErrVersionConflict) {
		t.Fatal("stale version list", err)
	}
	private := filepath.Join(root, "private-secret")
	if err := os.WriteFile(private, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(root, "sites", "demo", "secret-link")); err != nil {
		t.Skipf("symlink unavailable on this host: %v", err)
	}
	if files, err := s.FilesAs(ctx, "demo", owner, updated.Project.Deployments[0].ID); !errors.Is(err, project.ErrUsageUnavailable) || files != nil {
		t.Fatalf("symlink accepted: %+v %v", files, err)
	}
}
