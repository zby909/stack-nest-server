package resumable

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/storage"
)

// uploadFixture 使用真实磁盘文件和可控容量，便于模拟中断、重启及磁盘耗尽。
func uploadFixture(t *testing.T, content []byte) (*Store, Request) {
	t.Helper()
	guard := storage.New(t.TempDir())
	guard.Read = func(string) (storage.Usage, error) {
		return storage.Usage{TotalBytes: 10 << 30, FreeBytes: 8 << 30}, nil
	}
	sum := sha256.Sum256(content)
	return New(guard, storage.MaxArchiveBytes), Request{ID: uuid.NewString(), Kind: "create", Name: "Demo", Slug: "demo", FileName: "site.zip", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
}

// TestResumeWithEscapedMetadata 验证合法长说明经 JSON 转义后仍可恢复、续传和发布。
func TestResumeWithEscapedMetadata(t *testing.T) {
	content := []byte("site archive")
	store, input := uploadFixture(t, content)
	input.Name = strings.Repeat("<", 256)
	input.Description = strings.Repeat("&", 4096)
	input.Notes = strings.Repeat(">", 4096)
	input.FileName = strings.Repeat("&", 251) + ".zip"
	if _, err := store.Create("alice", input); err != nil {
		t.Fatal(err)
	}
	state, err := os.Stat(filepath.Join(store.dir, input.ID, "state.json"))
	if err != nil || state.Size() <= 16384 {
		t.Fatalf("fixture did not exceed the old read limit: %v %v", state, err)
	}
	store = New(store.guard, storage.MaxArchiveBytes)
	progress, err := store.Get(input.ID, "alice")
	if err != nil || progress.Request != input || progress.Offset != 0 {
		t.Fatalf("metadata lost after restart: %+v %v", progress, err)
	}
	if _, err := store.Create("alice", input); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if _, err := store.Append(context.Background(), input.ID, "alice", 0, content); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(context.Background(), input.ID, "alice", func(ticket Ticket, source io.Reader) (string, error) {
		got, err := io.ReadAll(source)
		if err != nil || !bytes.Equal(got, content) || ticket.Request != input {
			t.Fatalf("invalid publish data: %v", err)
		}
		return "project-id", nil
	}); err != nil {
		t.Fatal(err)
	}
	store = New(store.guard, storage.MaxArchiveBytes)
	if receipt, err := store.Get(input.ID, "alice"); err != nil || receipt.ResultID != "project-id" {
		t.Fatalf("publish receipt unreadable: %+v %v", receipt, err)
	}
}

// TestResumeAfterRestartAndUncommittedTail 验证重启后只认已提交分片，并覆盖崩溃留下的尾部。
func TestResumeAfterRestartAndUncommittedTail(t *testing.T) {
	content := append(bytes.Repeat([]byte("a"), int(ChunkSize)), []byte("ending")...)
	store, input := uploadFixture(t, content)
	if _, err := store.Create("alice", input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), input.ID, "alice", 0, content[:ChunkSize]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), input.ID, "alice", 0, content[:ChunkSize]); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate chunk accepted: %v", err)
	}
	path := filepath.Join(store.dir, input.ID, "archive")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.Write([]byte("bad"))
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	store = New(store.guard, storage.MaxArchiveBytes)
	progress, err := store.Get(input.ID, "alice")
	if err != nil || progress.Offset != ChunkSize {
		t.Fatalf("restart progress: %+v %v", progress, err)
	}
	if _, err := store.Append(context.Background(), input.ID, "alice", ChunkSize, content[ChunkSize:]); err != nil {
		t.Fatal(err)
	}
	calls := 0
	publish := func(_ Ticket, source io.Reader) (string, error) {
		calls++
		got, err := io.ReadAll(source)
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("corrupt recovered file: %v", err)
		}
		return "project-id", nil
	}
	for range 2 {
		if _, err := store.Publish(context.Background(), input.ID, "alice", publish); err != nil {
			t.Fatal(err)
		}
		store = New(store.guard, storage.MaxArchiveBytes)
	}
	if calls != 1 {
		t.Fatalf("published %d times", calls)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed archive was not cleaned")
	}
}

// TestUploadIsolationLimitsAndExpiration 验证跨用户不能读写、配额有效且到期可回收。
func TestUploadIsolationLimitsAndExpiration(t *testing.T) {
	store, input := uploadFixture(t, []byte("abc"))
	if _, err := store.Create("alice", input); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func() error{
		func() error { _, err := store.Get(input.ID, "bob"); return err },
		func() error {
			_, err := store.Append(context.Background(), input.ID, "bob", 0, []byte("abc"))
			return err
		},
		func() error { return store.Delete(input.ID, "bob") },
	} {
		if !errors.Is(operation(), ErrNotFound) {
			t.Fatal("cross-user upload access")
		}
	}
	second := input
	second.ID = uuid.NewString()
	if _, err := store.Create("alice", second); err != nil {
		t.Fatal(err)
	}
	third := input
	third.ID = uuid.NewString()
	if _, err := store.Create("alice", third); !errors.Is(err, ErrLimit) {
		t.Fatalf("quota not enforced: %v", err)
	}
	if _, err := store.Create("bob", third); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Now().Add(Lifetime + time.Minute) }
	if err := store.Cleanup(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(store.dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("expired files retained: %v %v", entries, err)
	}
}

// TestRejectCorruptionCanceledChunkAndFullDisk 验证失败分片不推进进度，完整文件摘要失败不会调用发布。
func TestRejectCorruptionCanceledChunkAndFullDisk(t *testing.T) {
	store, input := uploadFixture(t, []byte("abc"))
	if _, err := store.Create("alice", input); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Append(ctx, input.ID, "alice", 0, []byte("abc")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	store.guard.Read = func(string) (storage.Usage, error) {
		return storage.Usage{TotalBytes: 10 << 30, FreeBytes: 1 << 30}, nil
	}
	if _, err := store.Append(context.Background(), input.ID, "alice", 0, []byte("abc")); !errors.Is(err, storage.ErrInsufficient) {
		t.Fatal(err)
	}
	progress, _ := store.Get(input.ID, "alice")
	if progress.Offset != 0 {
		t.Fatal("failed chunk advanced progress")
	}
	store.guard.Read = func(string) (storage.Usage, error) {
		return storage.Usage{TotalBytes: 10 << 30, FreeBytes: 8 << 30}, nil
	}
	if _, err := store.Append(context.Background(), input.ID, "alice", 0, []byte("xyz")); err != nil {
		t.Fatal(err)
	}
	_, err := store.Publish(context.Background(), input.ID, "alice", func(Ticket, io.Reader) (string, error) { t.Fatal("published corrupt archive"); return "", nil })
	if !errors.Is(err, ErrChecksum) {
		t.Fatal(err)
	}
}
