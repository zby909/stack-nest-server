package project_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"stacknest/server/internal/project"
)

// TestEditPreservesDeployment 验证管理员和所有者可编辑，文件、身份和地址不受影响。
func TestEditPreservesDeployment(t *testing.T) {
	for _, actor := range []project.Actor{{ID: "test-owner"}, {ID: "admin", IsAdmin: true}} {
		s, store, _, root := setupProject(t)
		before := store.values["demo"]
		input := project.MetadataInput{Name: " 中文新名 ", Description: " 项目说明 ", ExpectedAt: before.UpdatedAt}
		updated, err := s.EditAs(context.Background(), "demo", actor, input)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Name != "中文新名" || updated.Description != "项目说明" || updated.Slug != before.Slug || updated.OwnerID != before.OwnerID || updated.ID != before.ID || !updated.CreatedAt.Equal(before.CreatedAt) || !reflect.DeepEqual(updated.Deployments, before.Deployments) || !updated.UpdatedAt.After(before.UpdatedAt) {
			t.Fatalf("unexpected metadata edit: %+v", updated)
		}
		assertSiteContent(t, root)
		if _, err := s.EditAs(context.Background(), "demo", actor, input); !errors.Is(err, project.ErrVersionConflict) {
			t.Fatalf("stale edit allowed: %v", err)
		}
		input.ExpectedAt, input.Description = updated.UpdatedAt, ""
		cleared, err := s.EditAs(context.Background(), "demo", actor, input)
		if err != nil || cleared.Description != "" {
			t.Fatalf("description cannot be cleared: %+v %v", cleared, err)
		}
	}
}

// TestEditRejectsInvalidOrUnauthorized 验证失败不会改变元数据和已发布内容。
func TestEditRejectsInvalidOrUnauthorized(t *testing.T) {
	for _, failure := range []string{"owner", "blank", "unicode-name", "description", "database", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			s, store, _, root := setupProject(t)
			before := store.values["demo"]
			input := project.MetadataInput{Name: "New", ExpectedAt: before.UpdatedAt}
			actor := project.Actor{ID: before.OwnerID}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var expected error
			switch failure {
			case "owner":
				actor.ID, expected = "another", project.ErrNotFound
			case "blank":
				input.Name, expected = " \t", project.ErrInvalidName
			case "unicode-name":
				input.Name, expected = strings.Repeat("中", 86), project.ErrInvalidName
			case "description":
				input.Description, expected = strings.Repeat("中", 1366), project.ErrInvalidDescription
			case "database":
				expected = errors.New("database unavailable")
				store.updateErr = expected
			case "cancel":
				cancel()
				expected = context.Canceled
			}
			if _, err := s.EditAs(ctx, "demo", actor, input); !errors.Is(err, expected) {
				t.Fatalf("wanted %v, got %v", expected, err)
			}
			if !reflect.DeepEqual(store.values["demo"], before) {
				t.Fatal("rejected edit changed metadata")
			}
			assertSiteContent(t, root)
		})
	}
}

// TestMetadataVersionSurvivesClockChange 发布不能复用编辑的时间版本，即使服务器时钟回退。
func TestMetadataVersionSurvivesClockChange(t *testing.T) {
	s, store, _, _ := setupProject(t)
	value := store.values["demo"]
	value.UpdatedAt = value.UpdatedAt.Add(time.Hour)
	store.values["demo"] = value
	owner := project.Actor{ID: value.OwnerID}
	updated, err := s.UpdateAs(context.Background(), "demo", owner, revisionInput(t, value.Deployments[0].ID, "new after edit"))
	if err != nil || !updated.Project.UpdatedAt.After(value.UpdatedAt) {
		t.Fatalf("publication reused an older metadata version: %+v %v", updated, err)
	}
	if _, err := s.EditAs(context.Background(), "demo", owner, project.MetadataInput{Name: "stale", ExpectedAt: value.UpdatedAt}); !errors.Is(err, project.ErrVersionConflict) {
		t.Fatalf("stale edit overwrote a newer deployment: %v", err)
	}
}

// readExportIndex 同时验证 ZIP 可正常打开、根入口内容正确且不会混入其他文件。
func readExportIndex(t *testing.T, archive project.Archive) string {
	t.Helper()
	data, err := io.ReadAll(archive)
	if err != nil || int64(len(data)) != archive.Size() {
		t.Fatalf("invalid archive size: %v", err)
	}
	reader := bytes.NewReader(data)
	files, err := zip.NewReader(reader, int64(len(data)))
	if err != nil || len(files.File) != 1 || files.File[0].Name != "index.html" {
		t.Fatalf("unexpected ZIP: %+v %v", files, err)
	}
	file, err := files.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// TestExportVersionsAndSnapshot 验证两版权限、失效编号和下载期间更新的快照一致性。
func TestExportVersionsAndSnapshot(t *testing.T) {
	s, store, _, root := setupProject(t)
	ctx := context.Background()
	owner := project.Actor{ID: "test-owner"}
	first := store.values["demo"].Deployments[0].ID
	second, err := s.UpdateAs(ctx, "demo", owner, revisionInput(t, first, "second"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first, second.Project.Deployments[0].ID} {
		if _, err := s.ExportAs(ctx, "demo", project.Actor{ID: "other"}, id); !errors.Is(err, project.ErrNotFound) {
			t.Fatalf("export leaked another user's project: %v", err)
		}
	}
	current, err := s.ExportAs(ctx, "demo", owner, second.Project.Deployments[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Archive.Close()
	previous, err := s.ExportAs(ctx, "demo", project.Actor{IsAdmin: true}, first)
	if err != nil {
		t.Fatal(err)
	}
	if got := readExportIndex(t, previous.Archive); got != "original site" {
		t.Fatalf("wrong previous: %s", got)
	}
	if err := previous.Archive.Close(); err != nil {
		t.Fatal(err)
	}
	// ZIP 完成后更新和删除均能继续；现有 ZIP 不再读取变化中的站点目录。
	if _, err := s.UpdateAs(ctx, "demo", owner, revisionInput(t, second.Project.Deployments[0].ID, "third")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportAs(ctx, "demo", owner, first); !errors.Is(err, project.ErrVersionConflict) {
		t.Fatalf("retired deployment exported: %v", err)
	}
	if err := s.DeleteAs(ctx, "demo", owner); err != nil {
		t.Fatal(err)
	}
	if got := readExportIndex(t, current.Archive); got != "second" {
		t.Fatalf("ZIP changed during download: %s", got)
	}
	if err := current.Archive.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "exports"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("export files not cleaned: %+v %v", entries, err)
	}
}
