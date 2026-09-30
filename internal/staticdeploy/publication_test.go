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

// TestPublicationRecovery 验证写库前后中断都能恢复，且不会遗留占用 slug 的孤立目录。
func TestPublicationRecovery(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before commit", true: "after commit"}[committed], func(t *testing.T) {
			root := t.TempDir()
			deployer := New(Config{DataDir: root, MaxFiles: 10, MaxBytes: 1024})
			if _, err := deployer.Publish(context.Background(), "demo", "site.zip", bytes.NewReader(testZip(t))); err != nil {
				t.Fatal(err)
			}
			restarted := New(Config{DataDir: root, MaxFiles: 10, MaxBytes: 1024})
			lookup := func(context.Context, string) (project.Project, error) {
				if committed {
					return project.Project{Slug: "demo"}, nil
				}
				return project.Project{}, project.ErrNotFound
			}
			if err := restarted.RecoverPublications(context.Background(), lookup); err != nil {
				t.Fatal(err)
			}
			_, err := os.Stat(filepath.Join(root, "sites", "demo", "index.html"))
			if (err == nil) != committed {
				t.Fatalf("unexpected published state: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "pending", "demo")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journal retained: %v", err)
			}
			if !committed {
				if _, err := restarted.Publish(context.Background(), "demo", "site.zip", bytes.NewReader(testZip(t))); err != nil {
					t.Fatalf("slug still stranded: %v", err)
				}
			}
		})
	}
}

// TestPublicationRecoveryPreservesUnknownOutcome 数据库暂时不可用时保留标记和所有文件。
func TestPublicationRecoveryPreservesUnknownOutcome(t *testing.T) {
	root := t.TempDir()
	deployer := New(Config{DataDir: root, MaxFiles: 10, MaxBytes: 1024})
	if _, err := deployer.Publish(context.Background(), "demo", "site.zip", bytes.NewReader(testZip(t))); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("database unavailable")
	err := deployer.RecoverPublications(context.Background(), func(context.Context, string) (project.Project, error) { return project.Project{}, cause })
	if !errors.Is(err, cause) {
		t.Fatal(err)
	}
	for _, path := range []string{"pending/demo", "sites/demo/index.html"} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRecoveryCleansOnlyOwnedTemporaryFiles 验证重启清理只处理已知的临时文件名。
func TestRecoveryCleansOnlyOwnedTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"tmp/receive-old", "tmp/upload-old", "sites/.publish-old", "tmp/keep-me"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	deployer := New(Config{DataDir: root})
	if err := deployer.RecoverPublications(context.Background(), func(context.Context, string) (project.Project, error) { return project.Project{}, project.ErrNotFound }); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"tmp/receive-old", "tmp/upload-old", "sites/.publish-old"} {
		if _, err := os.Stat(filepath.Join(root, dir)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary path retained: %s %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "tmp/keep-me")); err != nil {
		t.Fatal(err)
	}
}
