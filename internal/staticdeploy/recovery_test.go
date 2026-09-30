package staticdeploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/project"
)

// putRelease 创建用于模拟崩溃现场的不同版本。
func putRelease(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRevisionCrashCheckpoints 模拟目录切换和提交清理中断，重启后两版始终与数据库一致。
func TestRevisionCrashCheckpoints(t *testing.T) {
	for _, revert := range []bool{false, true} {
		for step := 0; step <= 5; step++ {
			t.Run(fmt.Sprintf("rollback=%v/step=%d", revert, step), func(t *testing.T) {
				root := t.TempDir()
				d := New(Config{DataDir: root})
				r := d.revision("demo")
				putRelease(t, r.current, "current")
				putRelease(t, r.previous, "previous")
				if err := os.MkdirAll(r.work, 0o700); err != nil {
					t.Fatal(err)
				}
				r.journal = revisionJournal{ProjectID: "project", FromID: "from", ToID: "to", Rollback: revert}
				if !revert {
					putRelease(t, filepath.Join(r.work, "next"), "next")
				}
				if err := r.writeJournal(); err != nil {
					t.Fatal(err)
				}
				if step >= 1 {
					target := "previous"
					if revert {
						target = "next"
					}
					if err := os.Rename(r.previous, filepath.Join(r.work, target)); err != nil {
						t.Fatal(err)
					}
				}
				if step >= 2 {
					if err := os.Rename(r.current, filepath.Join(r.work, "old")); err != nil {
						t.Fatal(err)
					}
				}
				if step >= 3 {
					if err := os.Rename(filepath.Join(r.work, "next"), r.current); err != nil {
						t.Fatal(err)
					}
				}
				// 第四步表示数据库已提交；第五步表示上一版已完成归位。
				committed := step >= 4
				if step == 5 {
					if err := os.Rename(filepath.Join(r.work, "old"), r.previous); err != nil {
						t.Fatal(err)
					}
				}
				lookup := func(context.Context, string) (project.Project, error) {
					id := "from"
					if committed {
						id = "to"
					}
					return project.Project{ID: "project", Deployments: []project.Deployment{{ID: id}}}, nil
				}
				for range 2 {
					if err := d.RecoverRevisions(context.Background(), lookup); err != nil {
						t.Fatal(err)
					}
				}
				wantCurrent, wantPrevious := "current", "previous"
				if committed {
					wantCurrent, wantPrevious = "next", "current"
					if revert {
						wantCurrent = "previous"
					}
				}
				for dir, want := range map[string]string{r.current: wantCurrent, r.previous: wantPrevious} {
					got, err := os.ReadFile(filepath.Join(dir, "index.html"))
					if err != nil || string(got) != want {
						t.Fatalf("got %q want %q err %v", got, want, err)
					}
				}
			})
		}
	}
}

// TestInterruptedUndo 可恢复撤销过程中断，上一版不会因二次恢复而丢失。
func TestInterruptedUndo(t *testing.T) {
	for _, revert := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback=%v", revert), func(t *testing.T) {
			root := t.TempDir()
			d := New(Config{DataDir: root})
			r := d.revision("demo")
			putRelease(t, r.current, "current") // 撤销已把 old 移回 current。
			putRelease(t, filepath.Join(r.work, "next"), "candidate")
			if !revert {
				putRelease(t, filepath.Join(r.work, "previous"), "previous")
			}
			r.journal = revisionJournal{ProjectID: "project", FromID: "from", ToID: "to", Rollback: revert}
			if err := r.writeJournal(); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(r.previous), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := d.RecoverRevisions(context.Background(), func(context.Context, string) (project.Project, error) {
				return project.Project{ID: "project", Deployments: []project.Deployment{{ID: "from"}}}, nil
			}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(r.previous, "index.html"))
			want := "previous"
			if revert {
				want = "candidate"
			}
			if err != nil || string(got) != want {
				t.Fatalf("previous lost: %q %v", got, err)
			}
		})
	}
}
