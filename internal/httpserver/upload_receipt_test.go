package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"stacknest/server/internal/project"
	"stacknest/server/internal/resumable"
)

// TestCompletedUploadReceipt 旧上传只能确认仍被保留的版本，不能把另一草稿误报为本次结果。
func TestCompletedUploadReceipt(t *testing.T) {
	for _, transport := range []string{"browser", "token"} {
		for _, action := range []string{"replace", "discard", "publish", "previous", "pruned"} {
			t.Run(transport+"/"+action, func(t *testing.T) {
				s, _, _, plain, original := automationServer(t)
				ctx := context.Background()
				actor := project.Actor{ID: original.OwnerID}
				prefix := "/api/uploads"
				// request 使用浏览器测试身份或真实 Token 路由发送同一上传协议。
				request := func(method, path string, body []byte) *httptest.ResponseRecorder {
					return resumableRequest(s, method, path, body, original.OwnerID, 0)
				}
				if transport == "token" {
					prefix = deployPrefix + "uploads"
					request = func(method, path string, body []byte) *httptest.ResponseRecorder {
						return deployRequest(s, method, path, plain, body)
					}
				}
				content := []byte("<p>first preview</p>")
				sum := sha256.Sum256(content)
				input := resumable.Request{ID: uuid.NewString(), Kind: "update", Preview: true, Slug: original.Slug, ExpectedID: project.CurrentID(original), FileName: "guide.html", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
				body, _ := json.Marshal(input)
				path := prefix + "/" + input.ID
				for _, step := range []struct {
					method, path string
					body         []byte
					status       int
				}{{"POST", prefix, body, 201}, {"PATCH", path, content, 200}, {"POST", path + "/complete", nil, 200}} {
					if response := request(step.method, step.path, step.body); response.Code != step.status {
						t.Fatal(response.Code, response.Body.String())
					}
				}
				id := "dep-" + input.ID
				var err error
				switch action {
				case "replace":
					_, err = s.projects.UpdateAs(ctx, original.Slug, actor, project.UpdateInput{Preview: true, ExpectedID: input.ExpectedID, ReplaceDraftID: id, FileName: "new.html", Source: strings.NewReader("new draft")})
				case "discard":
					_, err = s.projects.DiscardDraftAs(ctx, original.Slug, actor, id)
				default:
					var current project.CreatedProject
					current, err = s.projects.PublishDraftAs(ctx, original.Slug, actor, id, input.ExpectedID)
					for n := 0; err == nil && n < map[string]int{"previous": 1, "pruned": 2}[action]; n++ {
						current, err = s.projects.UpdateAs(ctx, original.Slug, actor, project.UpdateInput{ExpectedID: project.CurrentID(current.Project), FileName: "new.html", Source: strings.NewReader("new production")})
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				before, _ := s.projects.Get(ctx, original.Slug)
				want := 409
				if action == "publish" || action == "previous" {
					want = 200
				}
				for range 2 {
					response := request("POST", path+"/complete", nil)
					if response.Code != want {
						t.Fatalf("receipt returned %d, expected %d: %s", response.Code, want, response.Body.String())
					}
					if want == 409 && !strings.Contains(response.Body.String(), "本次上传的版本已被替换、丢弃或清理") {
						t.Fatal("retired receipt did not explain the version change")
					}
				}
				after, _ := s.projects.Get(ctx, original.Slug)
				left, _ := json.Marshal(before)
				right, _ := json.Marshal(after)
				if string(left) != string(right) {
					t.Fatal("confirming receipt mutated retained versions")
				}
			})
		}
	}
}
