package activity

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestPersistenceAndInterruptedTail 验证重启后可读取日志且不完整尾行不吞掉下一条记录。
func TestPersistenceAndInterruptedTail(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{ID: "first", CreatedAt: time.Now(), Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(store.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString(`{"id":"interrupted`)
	file.Close()
	restarted, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Append(Event{ID: "second", Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	items, err := restarted.List()
	if err != nil || len(items) != 2 || items[0].ID != "second" {
		t.Fatalf("unexpected records: %v %v", items, err)
	}
}

// TestRotationBounds 验证轮转后只返回最近一百条，并限制磁盘占用。
func TestRotationBounds(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		if err := store.Append(Event{ID: fmt.Sprint(i), Message: strings.Repeat("记录", 512)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.rotate(); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{ID: "latest"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.List()
	if err != nil || len(items) != 100 || items[0].ID != "latest" {
		t.Fatalf("unexpected rotated records: %d %v", len(items), err)
	}
}

// TestOversizedRequestDoesNotBreakLog 验证超长的删除路径不会让整个日志页无法读取。
func TestOversizedRequestDoesNotBreakLog(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{ID: "long-request", Slug: strings.Repeat("x", 128<<10), Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(Event{ID: "following-request", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	items, err := store.List()
	if err != nil || len(items) != 2 || items[0].ID != "following-request" {
		t.Fatalf("one long request broke the log: %v %v", items, err)
	}
}
