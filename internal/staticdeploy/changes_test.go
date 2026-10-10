package staticdeploy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestChangesUsesContentDigest 验证相同字节数的文件修改，以及新增、删除和取消请求。
func TestChangesUsesContentDigest(t *testing.T) {
	root := t.TempDir()
	d := New(Config{DataDir: root, MaxFiles: 10, MaxBytes: 1024})
	putRelease(t, filepath.Join(root, "sites", "demo"), "new")
	putRelease(t, filepath.Join(root, "previous", "demo"), "old")
	for file, content := range map[string]string{"sites/demo/new.css": "new", "previous/demo/old.css": "old", "sites/demo/same.txt": "same", "previous/demo/same.txt": "same"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	changes, err := d.Changes(context.Background(), "demo", "previous", "sites")
	if err != nil || len(changes) != 3 {
		t.Fatalf("changes: %+v %v", changes, err)
	}
	for index, kind := range []string{"modified", "added", "deleted"} {
		if changes[index].Kind != kind {
			t.Fatalf("unexpected change: %+v", changes[index])
		}
	}
	if changes[0].BeforeBytes != changes[0].AfterBytes {
		t.Fatal("fixture must verify equal size")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Changes(ctx, "demo", "previous", "sites"); err == nil {
		t.Fatal("ignored canceled request")
	}
}
