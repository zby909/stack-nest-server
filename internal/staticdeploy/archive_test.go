package staticdeploy

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"stacknest/server/internal/project"
)

func TestExtractZipRejectsPathTraversal(t *testing.T) {
	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "malicious.zip")

	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../escape.txt")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entry.Write([]byte("escape")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	err = extractZip(zipPath, filepath.Join(tempDir, "release"), 5000, 1024*1024)
	if !errors.Is(err, project.ErrInvalidArchive) {
		t.Fatalf("expected invalid archive error, got %v", err)
	}
}

func TestExtractZipRejectsSymlink(t *testing.T) {
	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "symlink.zip")

	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	header := &zip.FileHeader{Name: "link", Method: zip.Store}
	header.SetMode(os.ModeSymlink | 0o777)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatalf("create symlink entry: %v", err)
	}
	if _, err := entry.Write([]byte("/etc/passwd")); err != nil {
		t.Fatalf("write symlink entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	err = extractZip(zipPath, filepath.Join(tempDir, "release"), 5000, 1024*1024)
	if !errors.Is(err, project.ErrInvalidArchive) {
		t.Fatalf("expected invalid archive error, got %v", err)
	}
}

func TestExtractZipRejectsTooManyFiles(t *testing.T) {
	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "many-files.zip")

	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := writer.Create(name); err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	err = extractZip(zipPath, filepath.Join(tempDir, "release"), 2, 1024*1024)
	if !errors.Is(err, project.ErrInvalidArchive) {
		t.Fatalf("expected invalid archive error, got %v", err)
	}
}

func TestExtractZipRejectsOversizedContent(t *testing.T) {
	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "large-content.zip")

	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("large.bin")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := entry.Write(make([]byte, 1025)); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close zip file: %v", err)
	}

	err = extractZip(zipPath, filepath.Join(tempDir, "release"), 5000, 1024)
	if !errors.Is(err, project.ErrInvalidArchive) {
		t.Fatalf("expected invalid archive error, got %v", err)
	}
}
