package staticdeploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/project"
)

// TestDraftCrashCheckpoints 在每个重命名阶段模拟退出，恢复后正式内容与草稿均完整。
func TestDraftCrashCheckpoints(t *testing.T) {
	const id = "dep-95d8840a-27a1-42c9-a3a2-7686e9770874"
	for _, first := range []bool{false, true} {
		for step := 0; step <= 5; step++ {
			t.Run(fmt.Sprintf("first=%v/step=%d", first, step), func(t *testing.T) {
				d := New(Config{DataDir: t.TempDir()})
				r := d.revision("demo")
				from := "old"
				if first {
					from = ""
				} else {
					putRelease(t, r.current, "current")
					putRelease(t, r.previous, "previous")
				}
				draft := filepath.Join(r.drafts, id)
				putRelease(t, draft, "draft")
				for _, dir := range []string{r.work, filepath.Dir(r.current), filepath.Dir(r.previous)} {
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				r.journal = revisionJournal{ProjectID: "p", FromID: from, ToID: id, DraftID: id}
				if err := r.writeJournal(); err != nil {
					t.Fatal(err)
				}
				moves := [][2]string{{draft, filepath.Join(r.work, "next")}, {r.previous, filepath.Join(r.work, "previous")}, {r.current, filepath.Join(r.work, "old")}, {filepath.Join(r.work, "next"), r.current}}
				for i, pair := range moves {
					if step >= i+1 {
						if err := moveIfExists(pair[0], pair[1]); err != nil {
							t.Fatal(err)
						}
					}
				}
				lookup := func(context.Context, string) (project.Project, error) {
					value := project.Project{ID: "p", Draft: &project.Deployment{ID: id}}
					if from != "" {
						value.Deployments = []project.Deployment{{ID: from}}
					}
					if step == 5 {
						value.Deployments, value.Draft = []project.Deployment{{ID: id}}, nil
					}
					return value, nil
				}
				for range 2 {
					if err := d.RecoverRevisions(context.Background(), lookup); err != nil {
						t.Fatal(err)
					}
				}
				location := draft
				if step == 5 {
					location = r.current
				}
				content, err := os.ReadFile(filepath.Join(location, "index.html"))
				if err != nil || string(content) != "draft" {
					t.Fatalf("lost draft: %s %v", content, err)
				}
				if !first && step != 5 {
					for path, text := range map[string]string{r.current: "current", r.previous: "previous"} {
						content, err := os.ReadFile(filepath.Join(path, "index.html"))
						if err != nil || string(content) != text {
							t.Fatalf("lost published content: %s %v", content, err)
						}
					}
				}
			})
		}
	}
}
