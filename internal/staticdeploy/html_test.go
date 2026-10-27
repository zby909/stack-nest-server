package staticdeploy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestPublishSingleHTML 验证大小写扩展名、非 index 文件名与原始字节均可正确发布。
func TestPublishSingleHTML(t *testing.T) {
	content := []byte("\xef\xbb\xbf<!doctype html><html><meta charset=utf-8><style>body{color:red}</style><body>你好<script>document.title='demo'</script></body></html>")
	for _, name := range []string{"my-page.html", "页面.HTM", "index.HTML"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			deployer := New(Config{DataDir: dir, MaxFiles: 5000, MaxBytes: 200 << 20})
			stats, err := deployer.Publish(context.Background(), "demo", name, bytes.NewReader(content))
			if err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(filepath.Join(dir, "sites", "demo", "index.html"))
			if err != nil || !bytes.Equal(content, actual) {
				t.Fatal("content or entry changed", err)
			}
			files, err := os.ReadDir(filepath.Join(dir, "sites", "demo"))
			if err != nil || len(files) != 1 {
				t.Fatal("unexpected published files", err)
			}
			if stats.Count != 1 || stats.Bytes != int64(len(content)) {
				t.Fatal("incorrect file statistics", stats)
			}
		})
	}
}

// TestSingleHTMLBoundaries 确认空文件、发布容量限制和取消请求不产生公开站点。
func TestSingleHTMLBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
		limit   int64
		cancel  bool
	}{
		{"empty", nil, 1024, false}, {"oversized", []byte("<h1>Hello</h1>"), 4, false},
		{"cancelled", []byte("<h1>Hello</h1>"), 1024, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			_, err := New(Config{DataDir: dir, MaxFiles: 5000, MaxBytes: tc.limit}).Publish(ctx, "demo", "page.html", bytes.NewReader(tc.content))
			if err == nil {
				t.Fatal("invalid upload published")
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "sites", "demo")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("partial site is public")
			}
		})
	}
}
