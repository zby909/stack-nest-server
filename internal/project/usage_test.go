package project_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/project"
)

// TestFileUsageLifecycle 验证初次发布、更新、回退与暂停后的统计始终对应真实版本。
func TestFileUsageLifecycle(t *testing.T) {
	s, store, _, root := setupProject(t)
	ctx := context.Background()
	owner := project.Actor{ID: "test-owner"}
	original := store.values["demo"]
	assertUsage := func(value project.Project, sizes ...int64) {
		t.Helper()
		usage, err := s.FileUsageAs(ctx, value, owner)
		if err != nil || len(usage) != len(sizes) {
			t.Fatalf("usage=%+v err=%v", usage, err)
		}
		for i, size := range sizes {
			if got := usage[value.Deployments[i].ID]; got.Bytes != size || got.Count != 1 {
				t.Fatalf("version %d: got %+v want %d bytes, 1 file", i, got, size)
			}
		}
	}
	assertUsage(original, 13)
	updated, err := s.UpdateAs(ctx, "demo", owner, revisionInput(t, original.Deployments[0].ID, "second"))
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(updated.Project, 6, 13)
	if usage, err := s.FileUsageAs(ctx, original, owner); !errors.Is(err, project.ErrVersionConflict) || len(usage) != 0 {
		t.Fatalf("stale version received usage: %+v %v", usage, err)
	}
	rolled, err := s.RollbackAs(ctx, "demo", owner, updated.Project.Deployments[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(rolled.Project, 13, 6)
	paused, err := s.SetAccessAs(ctx, "demo", owner, true, rolled.Project.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	assertUsage(paused, 13, 6)
	if err := os.Remove(filepath.Join(root, "previous", "demo", "index.html")); err != nil {
		t.Fatal(err)
	}
	usage, err := s.FileUsageAs(ctx, paused, owner)
	if !errors.Is(err, project.ErrUsageUnavailable) || len(usage) != 1 || usage[paused.Deployments[0].ID].Bytes != 13 {
		t.Fatalf("missing previous files must not become zero: %+v %v", usage, err)
	}
}

// TestFileUsageBoundary 拒绝越权、取消、未完成事务和不安全链接，不返回部分误导数字。
func TestFileUsageBoundary(t *testing.T) {
	for _, scenario := range []string{"foreign", "anonymous", "admin", "canceled", "pending", "missing", "nested", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			s, store, _, root := setupProject(t)
			ctx := context.Background()
			actor := project.Actor{ID: "test-owner"}
			var wantErr error
			want := project.PublishedFiles{Bytes: 13, Count: 1}
			switch scenario {
			case "foreign":
				actor.ID, wantErr = "other", project.ErrNotFound
			case "anonymous":
				actor.ID, wantErr = "", project.ErrNotFound
			case "admin":
				actor = project.Actor{ID: "admin", IsAdmin: true}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				wantErr = context.Canceled
			case "pending":
				if err := os.MkdirAll(filepath.Join(root, "changes", "demo"), 0700); err != nil {
					t.Fatal(err)
				}
				wantErr = project.ErrReleaseBusy
			case "missing":
				if err := os.Remove(filepath.Join(root, "sites", "demo", "index.html")); err != nil {
					t.Fatal(err)
				}
				wantErr = project.ErrUsageUnavailable
			case "nested":
				assets := filepath.Join(root, "sites", "demo", "assets")
				if err := os.Mkdir(assets, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(assets, "main.js"), []byte("12345"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(assets, "empty.txt"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				want = project.PublishedFiles{Bytes: 18, Count: 3}
			case "symlink":
				outside := filepath.Join(root, "private.txt")
				if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "sites", "demo", "linked.txt")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				wantErr = project.ErrUsageUnavailable
			}
			value := store.values["demo"]
			usage, err := s.FileUsageAs(ctx, value, actor)
			if wantErr != nil {
				if !errors.Is(err, wantErr) || len(usage) != 0 {
					t.Fatalf("usage=%+v err=%v want=%v", usage, err, wantErr)
				}
			} else if err != nil || len(usage) != 1 || usage[value.Deployments[0].ID] != want {
				t.Fatalf("usage=%+v err=%v want=%+v", usage, err, want)
			}
		})
	}
}
