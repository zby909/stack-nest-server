package staticdeploy

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"stacknest/server/internal/project"
)

// testZip 创建只有首页的测试站点。
func testZip(t *testing.T) []byte {
	t.Helper()
	return zipEntries(t, map[string]string{"index.html": "<!doctype html><h1>test</h1>"})
}

// zipEntries 打包指定文件供发布流程验证。
func zipEntries(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for path, content := range entries {
		file, err := writer.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buffer.Bytes()
}

// TestPublishNestedRootStats 验证入口目录外的附带文件只参与解压限额，不计入已发布站点。
func TestPublishNestedRootStats(t *testing.T) {
	source := zipEntries(t, map[string]string{"site/index.html": "hello", "notes.txt": "unpublished"})
	ctx := context.Background()
	d := New(Config{DataDir: t.TempDir(), MaxFiles: 10, MaxBytes: 1024})
	stats, err := d.Publish(ctx, "nested", "site.zip", bytes.NewReader(source))
	if err != nil || stats != (project.PublishedFiles{Bytes: 5, Count: 1}) {
		t.Fatalf("published stats: %+v, %v", stats, err)
	}
	if err := d.CompletePublication("nested"); err != nil {
		t.Fatal(err)
	}
	if actual, err := d.Measure(ctx, "nested", false); err != nil || actual != stats {
		t.Fatalf("disk stats: %+v, %v", actual, err)
	}
	limited := New(Config{DataDir: t.TempDir(), MaxFiles: 10, MaxBytes: 5})
	if _, err := limited.Publish(ctx, "nested", "site.zip", bytes.NewReader(source)); err == nil {
		t.Fatal("extraction ignored bytes outside the published root")
	}
}

func TestPublishSameSlugConcurrently(t *testing.T) {
	deployer := New(Config{
		DataDir:  t.TempDir(),
		MaxFiles: 5000,
		MaxBytes: 200 * 1024 * 1024,
	})
	source := testZip(t)

	var wait sync.WaitGroup
	wait.Add(2)

	results := make(chan error, 2)
	for range 2 {
		go func() {
			defer wait.Done()
			_, err := deployer.Publish(context.Background(), "same-slug", "site.zip", bytes.NewReader(source))
			results <- err
		}()
	}

	wait.Wait()
	close(results)

	var successCount int
	var conflictCount int
	for err := range results {
		switch {
		case err == nil:
			successCount++
		case errors.Is(err, project.ErrAlreadyExists):
			conflictCount++
		default:
			t.Fatalf("unexpected publish error: %v", err)
		}
	}

	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("expected one success and one conflict, got success=%d conflict=%d", successCount, conflictCount)
	}
}
