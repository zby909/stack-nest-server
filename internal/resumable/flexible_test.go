package resumable

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// TestFlexibleChunksResume 验证小分片、重启续传和旧版大分片可以混用且内容不变。
func TestFlexibleChunksResume(t *testing.T) {
	content := bytes.Repeat([]byte("h"), int(ChunkSize+3*MinChunkSize+53))
	store, input := uploadFixture(t, content)
	input.FileName = "report.html"
	if _, err := store.Create("alice", input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(context.Background(), input.ID, "alice", 0, content[:MinChunkSize]); err != nil {
		t.Fatal(err)
	}
	store = New(store.guard, store.maxSize)
	for _, length := range []int64{0, MinChunkSize - 1, ChunkSize + 1} {
		if _, err := store.Append(context.Background(), input.ID, "alice", MinChunkSize, content[:length]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid chunk length %d: %v", length, err)
		}
		status, err := store.Get(input.ID, "alice")
		if err != nil || status.Offset != MinChunkSize {
			t.Fatal("rejected chunk changed committed offset", err)
		}
	}
	offset := MinChunkSize
	for _, length := range []int64{ChunkSize, MinChunkSize, MinChunkSize, 53} {
		ticket, err := store.Append(context.Background(), input.ID, "alice", offset, content[offset:offset+length])
		if err != nil || ticket.Offset != offset+length {
			t.Fatal("mixed chunk upload failed", err)
		}
		offset += length
	}
	if _, err := store.Publish(context.Background(), input.ID, "alice", func(_ Ticket, source io.Reader) (string, error) {
		actual, err := io.ReadAll(source)
		if err != nil || !bytes.Equal(actual, content) {
			t.Fatal("mixed chunks changed file contents", err)
		}
		return "html-deployment", nil
	}); err != nil {
		t.Fatal(err)
	}
}
