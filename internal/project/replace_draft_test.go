package project_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"stacknest/server/internal/project"
)

// draftInput 构造固定版本编号，便于验证响应丢失后的幂等确认。
func draftInput(current, old, content string) project.UpdateInput {
	return project.UpdateInput{Preview: true, ExpectedID: current, ReplaceDraftID: old, DeploymentID: "dep-" + uuid.NewString(), FileName: "guide.html", Source: strings.NewReader(content), Notes: content}
}

// TestReplaceDraftFailures 验证校验失败、取消、事务失败及提交后响应丢失都不会丢失仍有效的草稿。
func TestReplaceDraftFailures(t *testing.T) {
	for _, scenario := range []string{"archive", "cancel", "database", "committed"} {
		t.Run(scenario, func(t *testing.T) {
			s, store, _, data := setupProject(t)
			ctx := context.Background()
			p := store.values["demo"]
			actor := project.Actor{ID: p.OwnerID}
			first, err := s.UpdateAs(ctx, p.Slug, actor, draftInput(project.CurrentID(p), "", "old draft"))
			if err != nil {
				t.Fatal(err)
			}
			old := first.Project.Draft.ID
			input := draftInput(project.CurrentID(p), old, "new draft")
			switch scenario {
			case "archive":
				input.FileName, input.Source = "bad.zip", strings.NewReader("invalid archive")
			case "cancel":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			case "database":
				store.draftErr = errors.New("transaction rejected")
			case "committed":
				store.committedDraftErr = errors.New("commit response lost")
			}
			result, err := s.UpdateAs(ctx, p.Slug, actor, input)
			if scenario == "committed" {
				if err != nil || result.Project.Draft.ID != input.DeploymentID {
					t.Fatalf("committed replacement not confirmed: %+v %v", result, err)
				}
				previewContent(t, s, input.DeploymentID, "new draft")
				if _, err := s.OpenPreview(context.Background(), old); !errors.Is(err, project.ErrNotFound) {
					t.Fatal("old preview still active", err)
				}
				if _, err := os.Stat(filepath.Join(data, "drafts", p.Slug, old)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("old draft files not retired", err)
				}
				input.Source = nil
				if _, err := s.UpdateAs(context.Background(), p.Slug, actor, input); err != nil {
					t.Fatal("repeat replacement was not idempotent", err)
				}
			} else {
				if err == nil || store.values[p.Slug].Draft.ID != old {
					t.Fatal("failed replacement changed draft", err)
				}
				previewContent(t, s, old, "old draft")
				if _, err := os.Stat(filepath.Join(data, "drafts", p.Slug, input.DeploymentID)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed replacement retained uncommitted files", err)
				}
			}
			assertSiteContent(t, data)
			if project.CurrentID(store.values[p.Slug]) != project.CurrentID(p) {
				t.Fatal("replacement changed production")
			}
		})
	}
}

// TestReplaceDraftConcurrency 验证两个替换请求竞争同一草稿时只能一个成功，并且旧发布确认失效。
func TestReplaceDraftConcurrency(t *testing.T) {
	s, store, _, data := setupProject(t)
	p := store.values["demo"]
	actor := project.Actor{ID: p.OwnerID}
	ctx := context.Background()
	first, err := s.UpdateAs(ctx, p.Slug, actor, draftInput(project.CurrentID(p), "", "old"))
	if err != nil {
		t.Fatal(err)
	}
	old := first.Project.Draft.ID
	if _, err := s.UpdateAs(ctx, p.Slug, project.Actor{ID: "other"}, draftInput(project.CurrentID(p), old, "foreign")); !errors.Is(err, project.ErrNotFound) {
		t.Fatal("cross-owner replace accepted", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, content := range []string{"one", "two"} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := s.UpdateAs(ctx, p.Slug, actor, draftInput(project.CurrentID(p), old, content))
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	succeeded, conflicts := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, project.ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicts != 1 {
		t.Fatal("concurrent replacements lost updates", succeeded, conflicts)
	}
	if _, err := s.PublishDraftAs(ctx, p.Slug, actor, old, project.CurrentID(p)); !errors.Is(err, project.ErrVersionConflict) {
		t.Fatal("stale publish accepted", err)
	}
	entries, err := os.ReadDir(filepath.Join(data, "drafts", p.Slug))
	if err != nil || len(entries) != 1 {
		t.Fatal("wrong number of retained drafts", entries, err)
	}
	assertSiteContent(t, data)
}
