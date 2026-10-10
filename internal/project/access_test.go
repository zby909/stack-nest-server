package project_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"stacknest/server/internal/project"
)

// TestPausedProjectKeepsVersions 验证暂停后仍可更新回退导出，但必须显式恢复访问。
func TestPausedProjectKeepsVersions(t *testing.T) {
	s, store, deployer, root := setupProject(t)
	value := store.values["demo"]
	owner := project.Actor{ID: value.OwnerID}
	if _, err := s.SetAccessAs(context.Background(), "demo", project.Actor{ID: "other"}, true, value.UpdatedAt); !errors.Is(err, project.ErrNotFound) {
		t.Fatal(err)
	}
	paused, err := s.SetAccessAs(context.Background(), "demo", owner, true, value.UpdatedAt)
	if err != nil || paused.Status != "paused" {
		t.Fatalf("pause: %v", err)
	}
	if _, err := s.SetAccessAs(context.Background(), "demo", owner, false, value.UpdatedAt); !errors.Is(err, project.ErrVersionConflict) {
		t.Fatalf("stale: %v", err)
	}
	updated, err := s.UpdateAs(context.Background(), "demo", owner, revisionInput(t, value.Deployments[0].ID, "paused update"))
	if err != nil || updated.Project.Status != "paused" {
		t.Fatalf("update: %v", err)
	}
	reverted, err := s.RollbackAs(context.Background(), "demo", owner, updated.Project.Deployments[0].ID)
	if err != nil || reverted.Project.Status != "paused" {
		t.Fatalf("rollback: %v", err)
	}
	exported, err := s.ExportAs(context.Background(), "demo", owner, reverted.Project.Deployments[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	exported.Archive.Close()
	marker := filepath.Join(root, "paused", "demo")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("update exposed paused site", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := deployer.RecoverAccess(context.Background(), store.List); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("restart lost pause", err)
	}
	online, err := s.SetAccessAs(context.Background(), "demo", project.Actor{IsAdmin: true}, false, reverted.Project.UpdatedAt)
	if err != nil || online.Status != "online" {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resume: %v", err)
	}
}

type accessOutcomeStore struct {
	*testStore
	commit, uncertain, wrote bool
}

// UpdateMetadata 模拟状态保存后的连接丢失。
func (s *accessOutcomeStore) UpdateMetadata(ctx context.Context, value project.Project, expected time.Time) error {
	if s.commit {
		if err := s.testStore.UpdateMetadata(ctx, value, expected); err != nil {
			return err
		}
	}
	s.wrote = true
	return errors.New("connection lost")
}

// Get 模拟无法确认事务最终结果的数据库故障。
func (s *accessOutcomeStore) Get(ctx context.Context, slug string) (project.Project, error) {
	if s.wrote && s.uncertain {
		return project.Project{}, errors.New("offline")
	}
	return s.testStore.Get(ctx, slug)
}

// TestAccessCommitRecovery 暂停与恢复提交不确定时保持关闭，重启后以持久化状态为准。
func TestAccessCommitRecovery(t *testing.T) {
	for _, pause := range []bool{true, false} {
		for _, commit := range []bool{true, false} {
			for _, uncertain := range []bool{true, false} {
				_, store, deployer, root := setupProject(t)
				value := store.values["demo"]
				if !pause {
					value.Status = "paused"
					store.values["demo"] = value
					if err := deployer.SetPaused("demo", true); err != nil {
						t.Fatal(err)
					}
				}
				fault := &accessOutcomeStore{testStore: store, commit: commit, uncertain: uncertain}
				s := project.NewService(fault, deployer)
				_, err := s.SetAccessAs(context.Background(), "demo", project.Actor{IsAdmin: true}, pause, value.UpdatedAt)
				if (err != nil) != (!commit || uncertain) {
					t.Fatalf("pause=%v commit=%v uncertain=%v: %v", pause, commit, uncertain, err)
				}
				marker := filepath.Join(root, "paused", "demo")
				if uncertain {
					if _, err := os.Stat(marker); err != nil {
						t.Fatal("unknown commit exposed site")
					}
				}
				fault.uncertain = false
				if err := deployer.RecoverAccess(context.Background(), store.List); err != nil {
					t.Fatal(err)
				}
				_, statErr := os.Stat(marker)
				if (statErr == nil) != (store.values["demo"].Status == "paused") {
					t.Fatal("recovery disagrees with database")
				}
			}
		}
	}
}
