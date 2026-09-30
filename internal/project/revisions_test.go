package project_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/project"
)

// revisionInput 创建可辨认内容的有效更新包。
func revisionInput(t *testing.T, id, text string) project.UpdateInput {
	t.Helper()
	var source bytes.Buffer
	w := zip.NewWriter(&source)
	f, err := w.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return project.UpdateInput{ExpectedID: id, FileName: text + ".zip", Source: &source}
}

// assertRevisionFiles 同时检查在线版与私有上一版，防止只改元数据未改文件。
func assertRevisionFiles(t *testing.T, root, current, previous string) {
	t.Helper()
	for dir, want := range map[string]string{"sites": current, "previous": previous} {
		got, err := os.ReadFile(filepath.Join(root, dir, "demo", "index.html"))
		if want == "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || string(got) != want {
			t.Fatalf("%s: got=%q want=%q err=%v", dir, got, want, err)
		}
	}
}

// TestUpdateRollbackLifecycle 验证原址更新、两版保留、重复回退、旧请求拒绝和删除清理。
func TestUpdateRollbackLifecycle(t *testing.T) {
	s, store, _, root := setupProject(t)
	ctx := context.Background()
	original := store.values["demo"]
	owner := project.Actor{ID: original.OwnerID}
	current := original
	if _, err := s.RollbackAs(ctx, "demo", owner, current.Deployments[0].ID); !errors.Is(err, project.ErrNoPrevious) {
		t.Fatalf("first release rollback: %v", err)
	}
	for _, text := range []string{"second", "third"} {
		result, err := s.UpdateAs(ctx, "demo", owner, revisionInput(t, current.Deployments[0].ID, text))
		if err != nil {
			t.Fatal(err)
		}
		current = result.Project
		if current.ID != original.ID || current.OwnerID != original.OwnerID || current.Name != original.Name || current.Slug != original.Slug || current.CreatedAt != original.CreatedAt || len(current.Deployments) != 2 || result.Files.Bytes != int64(len(text)) {
			t.Fatalf("project identity or stats changed: %+v", result)
		}
	}
	assertRevisionFiles(t, root, "third", "second")
	if _, err := s.RollbackAs(ctx, "demo", owner, original.Deployments[0].ID); !errors.Is(err, project.ErrVersionConflict) {
		t.Fatalf("stale rollback accepted: %v", err)
	}
	for _, want := range []struct{ current, previous string }{{"second", "third"}, {"third", "second"}} {
		result, err := s.RollbackAs(ctx, "demo", owner, current.Deployments[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		current = result.Project
		assertRevisionFiles(t, root, want.current, want.previous)
	}
	if err := s.DeleteAs(ctx, "demo", owner); err != nil {
		t.Fatal(err)
	}
	assertRevisionFiles(t, root, "", "")
}

// TestRevisionFailuresKeepBothVersions 验证输入、归属和数据库失败均不损坏原两版。
func TestRevisionFailuresKeepBothVersions(t *testing.T) {
	for _, failure := range []string{"owner", "archive", "database", "cancel", "rollback-database"} {
		t.Run(failure, func(t *testing.T) {
			s, store, _, root := setupProject(t)
			owner := project.Actor{ID: "test-owner"}
			first := store.values["demo"].Deployments[0].ID
			updated, err := s.UpdateAs(context.Background(), "demo", owner, revisionInput(t, first, "second"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			input := revisionInput(t, updated.Project.Deployments[0].ID, "third")
			switch failure {
			case "owner":
				owner.ID = "other-owner"
			case "archive":
				input.Source = bytes.NewBufferString("invalid zip")
			case "database", "rollback-database":
				store.updateErr = errors.New("database unavailable")
			case "cancel":
				store.beforeUpdate = cancel
			}
			if failure == "rollback-database" {
				_, err = s.RollbackAs(ctx, "demo", owner, input.ExpectedID)
			} else {
				_, err = s.UpdateAs(ctx, "demo", owner, input)
			}
			if err == nil {
				t.Fatal("expected rejection")
			}
			assertRevisionFiles(t, root, "second", "original site")
			if store.values["demo"].Deployments[0].ID != input.ExpectedID {
				t.Fatal("failed request changed database version")
			}
		})
	}
}

// ReplaceDeployment 模拟数据库已提交但客户端丢失确认的两种结果。
func (s *outcomeStore) ReplaceDeployment(ctx context.Context, slug, ownerID, expectedID string, next project.Deployment) error {
	if s.fault != "revision" {
		return s.testStore.ReplaceDeployment(ctx, slug, ownerID, expectedID, next)
	}
	if s.committed {
		if err := s.testStore.ReplaceDeployment(ctx, slug, ownerID, expectedID, next); err != nil {
			return err
		}
	}
	s.wrote = true
	return errors.New("connection lost while committing revision")
}

// TestRevisionAmbiguousCommit 验证未知结果保留现场、阻止新写入，并由重启恢复判断真实提交状态。
func TestRevisionAmbiguousCommit(t *testing.T) {
	for _, committed := range []bool{false, true} {
		for _, unknown := range []bool{false, true} {
			t.Run(map[bool]string{true: "committed", false: "uncommitted"}[committed]+map[bool]string{true: "/unknown", false: "/known"}[unknown], func(t *testing.T) {
				_, memory, deployer, root := setupProject(t)
				store := &outcomeStore{testStore: memory, fault: "revision", committed: committed, uncertain: unknown}
				s := project.NewService(store, deployer)
				ctx := context.Background()
				id := memory.values["demo"].Deployments[0].ID
				_, err := s.UpdateAs(ctx, "demo", project.Actor{IsAdmin: true}, revisionInput(t, id, "second"))
				if (err == nil) != (committed && !unknown) {
					t.Fatalf("unexpected request outcome: %v", err)
				}
				if unknown {
					if _, err := deployer.StageDelete(ctx, "demo"); !errors.Is(err, project.ErrReleaseBusy) {
						t.Fatalf("pending transaction not protected: %v", err)
					}
					if err := deployer.RecoverRevisions(ctx, store.Get); err == nil {
						t.Fatal("recovery guessed without database")
					}
				}
				store.uncertain = false
				if err := deployer.RecoverRevisions(ctx, store.Get); err != nil {
					t.Fatal(err)
				}
				if committed {
					assertRevisionFiles(t, root, "second", "original site")
				} else {
					assertRevisionFiles(t, root, "original site", "")
				}
			})
		}
	}
}
