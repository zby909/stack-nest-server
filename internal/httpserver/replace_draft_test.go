package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/deployclient"
	"stacknest/server/internal/project"
	"stacknest/server/internal/resumable"
	"stacknest/server/internal/storage"
)

// previewStatus 经真实 Host 路由检查已经返回给用户的预览链接。
func previewStatus(s *Server, url string) int {
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	return w.Code
}

// TestCLIReplaceDraft 验证默认禁止覆盖、显式替换、响应丢失重试、预览失效以及继续发布同一新版本。
func TestCLIReplaceDraft(t *testing.T) {
	s, _, _, plain, original := automationServer(t)
	var drop atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/complete") && drop.CompareAndSwap(true, false) {
			s.http.Handler.ServeHTTP(httptest.NewRecorder(), r)
			w.WriteHeader(502)
			return
		}
		s.http.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, _ := deployclient.New(server.URL, plain)
	client.Wait = func(context.Context, time.Duration) error { return nil }
	dir := t.TempDir()
	file, state := filepath.Join(dir, "guide.html"), filepath.Join(dir, "first.json")
	if err := os.WriteFile(file, []byte("<p>first preview</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := client.Upload(context.Background(), file, state, "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("<p>edited preview</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	state = filepath.Join(dir, "edited.json")
	if _, err := client.Upload(context.Background(), file, state, "edited", nil); err == nil {
		t.Fatal("normal upload overwrote existing draft")
	}
	drop.Store(true)
	edited, err := client.UploadReplacing(context.Background(), file, state, "edited", first.DraftID, nil)
	if err != nil || edited.DraftID == first.DraftID || edited.PreviewURL == first.PreviewURL || edited.Status != "preview" {
		t.Fatalf("replacement failed: %+v %v", edited, err)
	}
	if previewStatus(s, first.PreviewURL) != 404 || previewStatus(s, edited.PreviewURL) != 200 {
		t.Fatal("retired and current preview routing incorrect")
	}
	value, _ := s.projects.Get(context.Background(), original.Slug)
	if project.CurrentID(value) != project.CurrentID(original) || value.Draft.ID != edited.DraftID {
		t.Fatal("replacement changed production or created another draft")
	}
	again, err := client.UploadReplacing(context.Background(), file, state, "edited", first.DraftID, nil)
	if err != nil || again != edited {
		t.Fatal("replacement repeat created another version", again, err)
	}
	if _, err := client.UploadReplacing(context.Background(), file, state, "edited", edited.DraftID, nil); err == nil {
		t.Fatal("receipt retargeted to a different draft")
	}
	if _, err := client.Publish(context.Background(), first.DraftID, first.ExpectedID); err == nil {
		t.Fatal("stale preview publish succeeded")
	}
	if _, err := client.Publish(context.Background(), edited.DraftID, edited.ExpectedID); err != nil {
		t.Fatal(err)
	}
	value, _ = s.projects.Get(context.Background(), original.Slug)
	if value.Draft != nil || len(value.Deployments) != 2 || project.CurrentID(value) != edited.DraftID {
		t.Fatal("replaced draft publication retained incorrect versions")
	}
}

// TestReplaceDraftAdmission 拒绝跨用户目标和把替换参数用于非预览或同一版本编号。
func TestReplaceDraftAdmission(t *testing.T) {
	s, _, _, plain, original := automationServer(t)
	ctx := context.Background()
	draft, err := s.projects.UpdateAs(ctx, original.Slug, project.Actor{ID: original.OwnerID}, project.UpdateInput{Preview: true, ExpectedID: project.CurrentID(original), FileName: "guide.html", Source: strings.NewReader("draft")})
	if err != nil {
		t.Fatal(err)
	}
	input := resumable.Request{ID: strings.TrimPrefix(draft.Project.Draft.ID, "dep-"), Kind: "update", Slug: original.Slug, ExpectedID: project.CurrentID(original), ReplaceDraftID: draft.Project.Draft.ID, Preview: true, FileName: "guide.html", Size: 1, SHA256: strings.Repeat("a", 64)}
	body, _ := json.Marshal(input)
	if w := deployRequest(s, "POST", deployPrefix+"uploads", plain, body); w.Code != 400 {
		t.Fatal("same deployment ID accepted", w.Code)
	}
	input.ID = "c260ac42-2201-4717-8613-c058bcb41cf1"
	input.Preview = false
	body, _ = json.Marshal(input)
	if w := deployRequest(s, "POST", deployPrefix+"uploads", plain, body); w.Code != 400 {
		t.Fatal("replacement allowed direct production upload", w.Code)
	}
	input.Preview = true
	body, _ = json.Marshal(input)
	if w := resumableRequest(s, "POST", "/api/uploads", body, "bob", 0); w.Code != 404 {
		t.Fatal("foreign owner can replace draft", w.Code)
	}
	s.config.Storage.Read = func(string) (storage.Usage, error) {
		return storage.Usage{TotalBytes: 1000, FreeBytes: 100}, nil
	}
	if w := deployRequest(s, "POST", deployPrefix+"uploads", plain, body); w.Code != 507 {
		t.Fatal("replacement bypassed disk guard", w.Code)
	}
	value, _ := s.projects.Get(ctx, original.Slug)
	if value.Draft.ID != draft.Project.Draft.ID {
		t.Fatal("insufficient space removed old draft")
	}
}

// TestReplacementInterruptedByPublication 验证传输期间草稿被发布或丢弃时，完成请求必须拒绝过期替换。
func TestReplacementInterruptedByPublication(t *testing.T) {
	for _, action := range []string{"publish", "discard"} {
		t.Run(action, func(t *testing.T) {
			s, _, _, plain, original := automationServer(t)
			ctx := context.Background()
			actor := project.Actor{ID: original.OwnerID}
			first, err := s.projects.UpdateAs(ctx, original.Slug, actor, project.UpdateInput{Preview: true, ExpectedID: project.CurrentID(original), FileName: "guide.html", Source: strings.NewReader("old")})
			if err != nil {
				t.Fatal(err)
			}
			content := []byte("replacement")
			sum := sha256.Sum256(content)
			input := resumable.Request{ID: uuid.NewString(), Kind: "update", Preview: true, Slug: original.Slug, ExpectedID: project.CurrentID(original), ReplaceDraftID: first.Project.Draft.ID, FileName: "guide.html", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
			body, _ := json.Marshal(input)
			if w := deployRequest(s, "POST", deployPrefix+"uploads", plain, body); w.Code != 201 {
				t.Fatal(w.Code, w.Body.String())
			}
			path := deployPrefix + "uploads/" + input.ID
			if w := deployRequest(s, "PATCH", path, plain, content); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if action == "publish" {
				_, err = s.projects.PublishDraftAs(ctx, original.Slug, actor, first.Project.Draft.ID, project.CurrentID(original))
			} else {
				_, err = s.projects.DiscardDraftAs(ctx, original.Slug, actor, first.Project.Draft.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if w := deployRequest(s, "POST", path+"/complete", plain, []byte("{}")); w.Code != 409 {
				t.Fatal("stale replacement accepted", w.Code, w.Body.String())
			}
			value, _ := s.projects.Get(ctx, original.Slug)
			if value.Draft != nil {
				t.Fatal("stale upload recreated discarded or published draft")
			}
		})
	}
}
