package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/project"
	"stacknest/server/internal/resumable"
)

// resumableRequest 通过真实路由发送请求，可切换身份验证会话隔离。
func resumableRequest(server *Server, method, path string, body []byte, owner string, offset int64) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if method == "PATCH" {
		r.Header.Set("Content-Type", "application/octet-stream")
		r.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	}
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, auth.User{ID: owner, Email: owner + "@example.com", Role: auth.RoleUser}))
	w := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(w, r)
	return w
}

// TestResumableEscapedMetadata 验证合法说明的 JSON 编码不会超出创建请求和记录读取的上限。
func TestResumableEscapedMetadata(t *testing.T) {
	s := newTestServer(t)
	content := staticZip(t)
	hash := sha256.Sum256(content)
	input := resumable.Request{
		ID: uuid.NewString(), Kind: "create", Name: "Demo", Slug: "demo",
		Description: strings.Repeat("&", 4096), Notes: strings.Repeat("<", 4096),
		FileName: "site.zip", Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:]),
	}
	body, err := json.Marshal(input)
	if err != nil || len(body) <= 16384 {
		t.Fatalf("fixture did not exceed the old request limit: %d %v", len(body), err)
	}
	for range 2 {
		if w := resumableRequest(s, "POST", "/api/uploads", body, "alice", 0); w.Code != 201 {
			t.Fatalf("create or retry: %d %s", w.Code, w.Body.String())
		}
	}
	if w := resumableRequest(s, "GET", "/api/uploads/"+input.ID, nil, "alice", 0); w.Code != 200 {
		t.Fatalf("get progress: %d %s", w.Code, w.Body.String())
	}
}

// TestResumableZIPAndRARLifecycle 验证真实归档发布、响应丢失后重试、权限和文件清单。
func TestResumableZIPAndRARLifecycle(t *testing.T) {
	for _, name := range []string{"site.zip", "site-v4.rar", "site-v5.rar"} {
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t)
			content := staticZip(t)
			if filepath.Ext(name) == ".rar" {
				var err error
				content, err = os.ReadFile(filepath.Join("..", "staticdeploy", "testdata", name))
				if err != nil {
					t.Fatal(err)
				}
			}
			hash := sha256.Sum256(content)
			input := resumable.Request{ID: uuid.NewString(), Kind: "create", Name: "Resumed", Slug: "resume-demo", FileName: name, Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}
			body, _ := json.Marshal(input)
			if w := resumableRequest(s, "POST", "/api/uploads", body, "alice", 0); w.Code != 201 {
				t.Fatalf("create: %d %s", w.Code, w.Body.String())
			}
			path := "/api/uploads/" + input.ID
			if w := resumableRequest(s, "PATCH", path, content, "bob", 0); w.Code != 404 {
				t.Fatalf("cross user: %d", w.Code)
			}
			if w := resumableRequest(s, "POST", path+"/complete", nil, "alice", 0); w.Code != 409 {
				t.Fatalf("early publish: %d", w.Code)
			}
			if w := resumableRequest(s, "PATCH", path, content, "alice", 0); w.Code != 200 {
				t.Fatalf("chunk: %d %s", w.Code, w.Body.String())
			}
			statePath := filepath.Join(s.config.Storage.Dir, "uploads", input.ID, "state.json")
			before, _ := os.ReadFile(statePath)
			for attempt := 0; attempt < 2; attempt++ {
				w := resumableRequest(s, "POST", path+"/complete", nil, "alice", 0)
				if w.Code != 200 {
					t.Fatalf("publish: %d %s", w.Code, w.Body.String())
				}
				if attempt == 0 {
					// 模拟发布已提交但完成收据尚未写入就崩溃；重试不能重新发布。
					if err := os.WriteFile(statePath, before, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(filepath.Dir(statePath), "archive"), content, 0o600); err != nil {
						t.Fatal(err)
					}
					s = New(s.config, s.projects, nil)
				}
			}
			value, _ := s.projects.Get(context.Background(), input.Slug)
			if len(value.Deployments) != 1 || value.Deployments[0].ID != "dep-"+input.ID {
				t.Fatalf("duplicate release: %+v", value)
			}
			filesPath := "/api/projects/" + input.Slug + "/files?deploymentId=" + value.Deployments[0].ID
			if w := resumableRequest(s, "GET", filesPath, nil, "bob", 0); w.Code != 404 {
				t.Fatalf("files leaked: %d", w.Code)
			}
			if w := resumableRequest(s, "GET", filesPath, nil, "alice", 0); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("index.html")) {
				t.Fatalf("files: %d %s", w.Code, w.Body.String())
			}
			if w := resumableRequest(s, "DELETE", path, nil, "alice", 0); w.Code != 204 {
				t.Fatal(w.Code)
			}
			if _, err := s.projects.Get(context.Background(), input.Slug); err != nil {
				t.Fatal("discard removed published project")
			}
		})
	}
}

// TestResumableUpdateIsIdempotent 验证更新重试不会挤掉上一版，过期更新不能覆盖新版本。
func TestResumableUpdateIsIdempotent(t *testing.T) {
	s := newTestServer(t)
	content := staticZip(t)
	created, err := s.projects.Create(context.Background(), project.CreateInput{OwnerID: "alice", OwnerEmail: "alice@example.com", Name: "Demo", Slug: "demo", FileName: "site.zip", Source: bytes.NewReader(content)})
	if err != nil {
		t.Fatal(err)
	}
	originalID := created.Project.Deployments[0].ID
	hash := sha256.Sum256(content)
	input := resumable.Request{ID: uuid.NewString(), Kind: "update", Slug: "demo", ExpectedID: originalID, FileName: "site.zip", Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}
	body, _ := json.Marshal(input)
	if w := resumableRequest(s, "POST", "/api/uploads", body, "alice", 0); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	path := "/api/uploads/" + input.ID
	if w := resumableRequest(s, "PATCH", path, content, "alice", 0); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for range 2 {
		if w := resumableRequest(s, "POST", path+"/complete", nil, "alice", 0); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	value, _ := s.projects.Get(context.Background(), "demo")
	if len(value.Deployments) != 2 || value.Deployments[1].ID != originalID {
		t.Fatal("retry destroyed previous version")
	}
	input.ID = uuid.NewString()
	body, _ = json.Marshal(input)
	if w := resumableRequest(s, "POST", "/api/uploads", body, "alice", 0); w.Code != 409 {
		t.Fatalf("stale version accepted: %d", w.Code)
	}
	// 只提交一个不完整请求体，服务端不能把它计为已完成分片。
	input.ExpectedID = value.Deployments[0].ID
	body, _ = json.Marshal(input)
	if w := resumableRequest(s, "POST", "/api/uploads", body, "alice", 0); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	r := httptest.NewRequest("PATCH", "/api/uploads/"+input.ID, &brokenChunk{Reader: bytes.NewReader(content[:10])})
	r.Header.Set("Content-Type", "application/octet-stream")
	r.Header.Set("Upload-Offset", "0")
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, auth.User{ID: "alice"}))
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	progress, _ := s.uploads.Get(input.ID, "alice")
	if progress.Offset != 0 {
		t.Fatal("broken request advanced offset")
	}
}

type brokenChunk struct{ io.Reader }

// Read 模拟 TCP 在一个分片中途断开。
func (b *brokenChunk) Read(data []byte) (int, error) {
	n, err := b.Reader.Read(data)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
