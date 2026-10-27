package resumable

import (
	"bytes"
	"context"
	"io"
	"testing"
)

// TestResumeHTML 保证单 HTML 与压缩包共用校验、偏移恢复和完成回执。
func TestResumeHTML(t *testing.T) {
	content := []byte("<!doctype html><h1>Resumable</h1>")
	for _, name := range []string{"page.html", "report.HTM"} {
		t.Run(name, func(t *testing.T) {
			store, input := uploadFixture(t, content)
			input.FileName = name
			if _, err := store.Create("alice", input); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Append(context.Background(), input.ID, "alice", 0, content); err != nil {
				t.Fatal(err)
			}
			store = New(store.guard, store.maxSize)
			if _, err := store.Publish(context.Background(), input.ID, "alice", func(ticket Ticket, source io.Reader) (string, error) {
				actual, err := io.ReadAll(source)
				if err != nil || !bytes.Equal(actual, content) || ticket.Request.FileName != name {
					t.Fatal("HTML upload changed", err)
				}
				return "html-deployment", nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
