package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"stacknest/server/internal/resumable"
)

// TestSmallChunksPublishHTML 验证完整 HTTP 链路接受小分片并原样发布 3.3 MiB HTML。
func TestSmallChunksPublishHTML(t *testing.T) {
	server := newTestServer(t)
	content := append([]byte("<!doctype html><h1>Trip</h1>"), bytes.Repeat([]byte(" "), 3_396_472)...)
	hash := sha256.Sum256(content)
	input := resumable.Request{ID: uuid.NewString(), Kind: "create", Name: "Trip", Slug: "small-chunks", FileName: "行程.html", Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}
	body, _ := json.Marshal(input)
	response := resumableRequest(server, "POST", "/api/uploads", body, "alice", 0)
	if response.Code != http.StatusCreated {
		t.Fatal(response.Body.String())
	}
	var status uploadStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.MinChunkSize != resumable.MinChunkSize || status.ChunkSize != resumable.ChunkSize {
		t.Fatal("small chunk capability missing", err)
	}
	path := "/api/uploads/" + input.ID
	for offset := 0; offset < len(content); {
		end := min(offset+(128<<10), len(content))
		response = resumableRequest(server, "PATCH", path, content[offset:end], "alice", int64(offset))
		if response.Code != http.StatusOK {
			t.Fatal(response.Body.String())
		}
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.Offset != int64(end) {
			t.Fatal("incorrect acknowledged offset", err)
		}
		offset = end
	}
	if response = resumableRequest(server, "POST", path+"/complete", nil, "alice", 0); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://stack-nest-small-chunks.localhost:8080/", nil))
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), content) {
		t.Fatal("published HTML differs from original")
	}
}
