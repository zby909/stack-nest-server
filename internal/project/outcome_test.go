package project_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/project"
	"stacknest/server/internal/staticdeploy"
)

type outcomeStore struct {
	*testStore
	fault     string
	committed bool
	uncertain bool
	wrote     bool
}

// Get 模拟提交报错后无法立即查询数据库的情况。
func (s *outcomeStore) Get(ctx context.Context, slug string) (project.Project, error) {
	if s.wrote && s.uncertain {
		return project.Project{}, errors.New("database temporarily unreachable")
	}
	return s.testStore.Get(ctx, slug)
}

// Create 模拟数据库提交前或提交后丢失连接。
func (s *outcomeStore) Create(ctx context.Context, value project.Project) error {
	if s.fault != "create" {
		return s.testStore.Create(ctx, value)
	}
	if s.committed {
		if err := s.testStore.Create(ctx, value); err != nil {
			return err
		}
	}
	s.wrote = true
	return errors.New("connection lost while committing create")
}

// Delete 模拟删除事务的提交结果未知。
func (s *outcomeStore) Delete(ctx context.Context, slug, ownerID string) error {
	if s.committed {
		if err := s.testStore.Delete(ctx, slug, ownerID); err != nil {
			return err
		}
	}
	s.wrote = true
	return errors.New("connection lost while committing delete")
}

// TestAmbiguousCommitRecovery 验证提交后报错及暂时查不到结果时，文件最终与数据库保持一致。
func TestAmbiguousCommitRecovery(t *testing.T) {
	for _, operation := range []string{"create", "delete"} {
		for _, scenario := range []struct {
			name      string
			committed bool
			uncertain bool
		}{
			{"committed-confirmed", true, false},
			{"committed-unknown", true, true},
			{"uncommitted-unknown", false, true},
		} {
			t.Run(operation+"/"+scenario.name, func(t *testing.T) {
				root := t.TempDir()
				store := &outcomeStore{testStore: &testStore{values: make(map[string]project.Project)}, committed: scenario.committed, uncertain: scenario.uncertain}
				deployer := staticdeploy.New(staticdeploy.Config{DataDir: root, MaxFiles: 10, MaxBytes: 1024})
				service := project.NewService(store, deployer)
				if operation == "delete" {
					if _, err := service.Create(context.Background(), projectInput(t)); err != nil {
						t.Fatal(err)
					}
				}
				store.fault = operation
				var err error
				if operation == "create" {
					_, err = service.Create(context.Background(), projectInput(t))
				} else {
					err = service.DeleteAs(context.Background(), "demo", project.Actor{IsAdmin: true})
				}
				if (err != nil) != scenario.uncertain {
					t.Fatalf("unexpected operation result: %v", err)
				}
				if scenario.uncertain {
					marker := "pending"
					if operation == "delete" {
						marker = "trash"
					}
					if _, err := os.Stat(filepath.Join(root, marker, "demo")); err != nil {
						t.Fatalf("recovery state lost: %v", err)
					}
				}
				store.uncertain = false
				if err := deployer.RecoverDeletions(context.Background(), store.Get); err != nil {
					t.Fatal(err)
				}
				if err := deployer.RecoverPublications(context.Background(), store.Get); err != nil {
					t.Fatal(err)
				}
				shouldExist := scenario.committed == (operation == "create")
				_, dbErr := store.Get(context.Background(), "demo")
				_, fileErr := os.Stat(filepath.Join(root, "sites", "demo", "index.html"))
				if shouldExist {
					if dbErr != nil || fileErr != nil {
						t.Fatalf("committed site lost: db=%v file=%v", dbErr, fileErr)
					}
				} else if !errors.Is(dbErr, project.ErrNotFound) || !errors.Is(fileErr, os.ErrNotExist) {
					t.Fatalf("orphaned data after recovery: db=%v file=%v", dbErr, fileErr)
				}
			})
		}
	}
}
