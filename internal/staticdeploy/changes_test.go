package staticdeploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChangesComparesContent 验证同大小和不同大小的修改，以及首次发布和取消。
func TestChangesComparesContent(t *testing.T) {
	root := t.TempDir()
	d := New(Config{DataDir: root, MaxFiles: 10, MaxBytes: 128 << 10})
	prefix := strings.Repeat("x", 32<<10)
	putRelease(t, filepath.Join(root, "sites", "demo"), prefix+"new")
	putRelease(t, filepath.Join(root, "previous", "demo"), prefix+"old")
	for file, content := range map[string]string{"sites/demo/new.css": "new", "previous/demo/old.css": "old", "sites/demo/same.txt": "same", "previous/demo/same.txt": "same", "sites/demo/size.txt": "larger", "previous/demo/size.txt": ""} {
		if err := os.WriteFile(filepath.Join(root, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	changes, err := d.Changes(context.Background(), "demo", "previous", "sites")
	if err != nil || len(changes) != 4 {
		t.Fatalf("changes: %+v %v", changes, err)
	}
	for index, kind := range []string{"modified", "added", "deleted", "modified"} {
		if changes[index].Kind != kind {
			t.Fatalf("unexpected change: %+v", changes[index])
		}
	}
	if changes[0].BeforeBytes != changes[0].AfterBytes {
		t.Fatal("fixture must verify equal size")
	}
	first, err := d.Changes(context.Background(), "demo", "", "sites")
	if err != nil || len(first) != 4 {
		t.Fatalf("first release: %+v %v", first, err)
	}
	for _, change := range first {
		if change.Kind != "added" {
			t.Fatalf("unexpected first release change: %+v", change)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Changes(ctx, "demo", "previous", "sites"); err == nil {
		t.Fatal("ignored canceled request")
	}
}
