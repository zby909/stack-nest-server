package staticdeploy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwaples/rardecode/v2"

	"stacknest/server/internal/project"
)

// TestExtractRAR 验证真实 RAR4 和 RAR5 包中的站点文件可以解压。
func TestExtractRAR(t *testing.T) {
	for _, version := range []string{"v4", "v5"} {
		t.Run(version, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "release")
			archive := filepath.Join("testdata", "site-"+version+".rar")
			if err := extractRAR(archive, destination, 5000, 200*1024*1024); err != nil {
				t.Fatal(err)
			}
			root, err := detectSiteRoot(destination)
			if err != nil {
				t.Fatal(err)
			}
			page, err := os.ReadFile(filepath.Join(root, "index.html"))
			if err != nil || !strings.Contains(string(page), "RAR"+strings.TrimPrefix(version, "v")+" Test") {
				t.Fatalf("unexpected RAR page: %s, %v", page, err)
			}
			if version == "v5" {
				if _, err := os.Stat(filepath.Join(root, "assets", "app.js")); err != nil {
					t.Fatalf("RAR asset missing: %v", err)
				}
			}
		})
	}
}

// TestExtractRARRejectsLimits 验证 RAR 与 ZIP 使用相同的文件数和解压大小上限。
func TestExtractRARRejectsLimits(t *testing.T) {
	archive := filepath.Join("testdata", "site-v5.rar")
	for _, test := range []struct {
		name     string
		maxFiles int
		maxBytes int64
	}{
		{"file count", 1, 200 * 1024 * 1024},
		{"expanded size", 5000, 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := extractRAR(archive, filepath.Join(t.TempDir(), "release"), test.maxFiles, test.maxBytes)
			if !errors.Is(err, project.ErrInvalidArchive) {
				t.Fatalf("expected invalid archive, got %v", err)
			}
		})
	}
}

// TestRARRejectsLinksAndEncryption 验证 RAR 重定向链接和加密条目不能发布。
func TestRARRejectsLinksAndEncryption(t *testing.T) {
	for _, header := range []*rardecode.FileHeader{
		{Name: "link", LinkType: rardecode.LinkTypeHardLink},
		{Name: "link", LinkType: rardecode.LinkTypeUnixSymlink},
		{Name: "secret", Encrypted: true},
		{Name: "directory", IsDir: true, UnPackedSize: 1},
	} {
		if err := validateRARHeader(header); !errors.Is(err, project.ErrInvalidArchive) {
			t.Fatalf("expected rejected RAR header %+v, got %v", header, err)
		}
	}
}

// TestExtractRARRejectsEncryptedArchive 验证带加密文件头的真实 RAR 得到明确错误。
func TestExtractRARRejectsEncryptedArchive(t *testing.T) {
	err := extractRAR(filepath.Join("testdata", "site-encrypted.rar"), filepath.Join(t.TempDir(), "release"), 5000, 200*1024*1024)
	if !errors.Is(err, project.ErrInvalidArchive) || !strings.Contains(err.Error(), "加密") {
		t.Fatalf("expected encrypted RAR rejection, got %v", err)
	}
}

// TestArchiveTargetRejectsUnsafeNames 验证两种系统的路径穿越与绝对路径均被拒绝。
func TestArchiveTargetRejectsUnsafeNames(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape", `..\escape`, "/absolute", `C:\temp\file`, `\\server\share`, "dir/../../escape", "."} {
		if _, err := archiveTarget(root, name, "RAR"); !errors.Is(err, project.ErrInvalidArchive) {
			t.Fatalf("expected %q to be rejected, got %v", name, err)
		}
	}
	target, err := archiveTarget(root, `dist\index.html`, "RAR")
	if err != nil || target != filepath.Join(root, "dist", "index.html") {
		t.Fatalf("unexpected normalized target %q, %v", target, err)
	}
}
