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

func testZip(t *testing.T) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("index.html")
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := file.Write([]byte("<!doctype html><h1>test</h1>")); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buffer.Bytes()
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
