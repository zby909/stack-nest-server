package staticdeploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

// TestExportRejectsUnsafeFiles 验证目录链接、超限文件、恢复事务和取消不会留下 ZIP。
func TestExportRejectsUnsafeFiles(t *testing.T) {
	for _, failure := range []string{"large", "count", "link", "pending", "cancel", "disk"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			site := filepath.Join(root, "sites", "demo")
			if err := os.MkdirAll(site, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(site, "index.html"), []byte("original"), 0o644); err != nil {
				t.Fatal(err)
			}
			guard := storage.New(root)
			guard.Read = func(string) (storage.Usage, error) {
				return storage.Usage{TotalBytes: 1 << 30, FreeBytes: 900 << 20}, nil
			}
			d := New(Config{DataDir: root, MaxBytes: 32, MaxFiles: 1, Guard: guard})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := project.ErrExportUnavailable
			switch failure {
			case "large":
				d.config.MaxBytes = 2
			case "count":
				_ = os.WriteFile(filepath.Join(site, "extra.txt"), []byte("extra"), 0o644)
			case "link":
				outside := filepath.Join(root, "secret")
				_ = os.WriteFile(outside, []byte("secret"), 0o600)
				if err := os.Symlink(outside, filepath.Join(site, "secret")); err != nil {
					t.Skipf("symlinks not permitted: %v", err)
				}
			case "pending":
				_ = os.MkdirAll(filepath.Join(root, "changes", "demo"), 0o700)
				want = project.ErrReleaseBusy
			case "cancel":
				cancel()
				want = context.Canceled
			case "disk":
				guard.Read = func(string) (storage.Usage, error) {
					return storage.Usage{TotalBytes: 1 << 30, FreeBytes: 50 << 20}, nil
				}
				want = storage.ErrInsufficient
			}
			if archive, err := d.Export(ctx, "demo", false); !errors.Is(err, want) {
				if archive != nil {
					_ = archive.Close()
				}
				t.Fatalf("expected %v, got %v", want, err)
			}
			entries, err := os.ReadDir(filepath.Join(root, "exports"))
			if err != nil && !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
				t.Fatalf("failed export left files: %v %v", entries, err)
			}
		})
	}
}

// TestRecoverExports 只清理本功能生成的临时文件，保留不匹配的文件和目录。
func TestRecoverExports(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "exports")
	_ = os.MkdirAll(filepath.Join(directory, "snapshot-directory.zip"), 0o700)
	for _, name := range []string{"snapshot-crash.zip", "keep.txt"} {
		_ = os.WriteFile(filepath.Join(directory, name), []byte("data"), 0o600)
	}
	if err := New(Config{DataDir: root}).RecoverExports(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		t.Fatalf("wrong export cleanup: %+v %v", entries, err)
	}
}
