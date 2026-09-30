package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

// revisionRequest 创建真实文件表单和带版本的同源请求。
func revisionRequest(t *testing.T, slug, id string, archive []byte, revert bool) *http.Request {
	t.Helper()
	path := "/api/projects/" + slug + "/rollback"
	var body bytes.Buffer
	contentType := ""
	if !revert {
		path = "/api/projects/" + slug + "/deployments"
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "revision.zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(archive); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		contentType = writer.FormDataContentType()
	}
	r := httptest.NewRequest(http.MethodPost, path, &body)
	r.Host = "localhost:8080"
	r.Header.Set("Origin", "http://localhost:8080")
	r.Header.Set("If-Match", strconv.Quote(id))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

// TestUpdateAdmission 拒绝无权限、过期版本、磁盘满和并发请求时不读取上传正文。
func TestUpdateAdmission(t *testing.T) {
	for _, scenario := range []string{"owner", "version", "disk", "busy"} {
		t.Run(scenario, func(t *testing.T) {
			s := newTestServer(t)
			created := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(created, createProjectRequest(t, "Admission", "admission"))
			var p project.Project
			if err := json.Unmarshal(created.Body.Bytes(), &p); err != nil || created.Code != 201 {
				t.Fatalf("create: %d %v", created.Code, err)
			}
			body := &unreadBody{}
			r := httptest.NewRequest(http.MethodPost, "/api/projects/admission/deployments", body)
			r.Header.Set("If-Match", strconv.Quote(p.Deployments[0].ID))
			want := 0
			switch scenario {
			case "owner":
				r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, auth.User{ID: "other-owner", Role: auth.RoleUser}))
				want = http.StatusNotFound
			case "version":
				r.Header.Set("If-Match", `"stale-version"`)
				want = http.StatusConflict
			case "disk":
				s.config.Storage.Read = func(string) (storage.Usage, error) { return storage.Usage{TotalBytes: 1000, FreeBytes: 100}, nil }
				want = http.StatusInsufficientStorage
			case "busy":
				s.uploadMu.Lock()
				defer s.uploadMu.Unlock()
				want = http.StatusTooManyRequests
			}
			response := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(response, r)
			if response.Code != want || body.reads != 0 {
				t.Fatalf("admission status=%d want=%d bodyReads=%d %s", response.Code, want, body.reads, response.Body.String())
			}
		})
	}
}

// TestRevisionHTTPFlow 验证 API 返回、公开站点、失败日志和磁盘阈值下的无复制回退。
func TestRevisionHTTPFlow(t *testing.T) {
	s := newTestServer(t)
	log, err := activity.New(s.config.Storage.Dir)
	if err != nil {
		t.Fatal(err)
	}
	s.config.Activity = log
	created := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(created, createProjectRequest(t, "Revisions", "revisions"))
	var p project.Project
	if err := json.Unmarshal(created.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	for _, valid := range []bool{false, true} {
		archive := []byte("bad archive")
		want := 400
		if valid {
			archive, want = zipWithFile(t, "index.html", "new version"), 200
		}
		response := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(response, revisionRequest(t, p.Slug, p.Deployments[0].ID, archive, false))
		if response.Code != want {
			t.Fatalf("update: %d %s", response.Code, response.Body.String())
		}
		if valid {
			if err := json.Unmarshal(response.Body.Bytes(), &p); err != nil || len(p.Deployments) != 2 {
				t.Fatalf("update response: %s", response.Body.String())
			}
		}
	}
	preview := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(preview, previewRequest(p.Slug))
	if preview.Code != 200 || preview.Body.String() != "new version" {
		t.Fatalf("updated preview: %s", preview.Body.String())
	}
	// 回退只重命名已有文件，即使暂停上传也仍可使用。
	s.config.Storage.Read = func(string) (storage.Usage, error) { return storage.Usage{TotalBytes: 1000, FreeBytes: 100}, nil }
	rolled := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rolled, revisionRequest(t, p.Slug, p.Deployments[0].ID, nil, true))
	if rolled.Code != 200 {
		t.Fatalf("rollback: %d %s", rolled.Code, rolled.Body.String())
	}
	preview = httptest.NewRecorder()
	s.http.Handler.ServeHTTP(preview, previewRequest(p.Slug))
	if !strings.Contains(preview.Body.String(), "StackNest Test") {
		t.Fatal("rollback did not restore original site")
	}
	events, err := log.List()
	if err != nil || len(events) != 4 || events[0].Action != "rollback" || events[1].Action != "update" || events[2].Status != "failed" || events[1].ExpandedBytes != int64(len("new version")) {
		t.Fatalf("revision audit: %+v %v", events, err)
	}
}

// exerciseAuthenticatedRevisions 用真实会话验证项目所有者和管理员可操作，其他用户不可操作。
func exerciseAuthenticatedRevisions(t *testing.T, s *Server, user, other, admin *http.Cookie) {
	t.Helper()
	value, err := s.projects.Get(context.Background(), "ordinary-site")
	if err != nil {
		t.Fatal(err)
	}
	id := value.Deployments[0].ID
	for _, revert := range []bool{false, true} {
		for _, cookie := range []*http.Cookie{nil, other} {
			r := revisionRequest(t, value.Slug, id, staticZip(t), revert)
			want := 401
			if cookie != nil {
				r.AddCookie(cookie)
				want = 404
			}
			response := httptest.NewRecorder()
			s.http.Handler.ServeHTTP(response, r)
			if response.Code != want {
				t.Fatalf("revision access: got=%d want=%d", response.Code, want)
			}
		}
	}
	for _, cookie := range []*http.Cookie{user, admin} {
		r := revisionRequest(t, value.Slug, id, zipWithFile(t, "index.html", "authenticated update"), false)
		r.AddCookie(cookie)
		response := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(response, r)
		if response.Code != 200 {
			t.Fatalf("authorized update: %d %s", response.Code, response.Body.String())
		}
		var updated project.Project
		if err := json.Unmarshal(response.Body.Bytes(), &updated); err != nil || updated.ID != value.ID || updated.OwnerID != value.OwnerID {
			t.Fatal("admin update changed project ownership")
		}
		stale := revisionRequest(t, value.Slug, id, nil, true)
		stale.AddCookie(cookie)
		staleResponse := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(staleResponse, stale)
		if staleResponse.Code != 409 {
			t.Fatal("stale rollback accepted")
		}
		r = revisionRequest(t, value.Slug, updated.Deployments[0].ID, nil, true)
		r.AddCookie(cookie)
		response = httptest.NewRecorder()
		s.http.Handler.ServeHTTP(response, r)
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &updated) != nil {
			t.Fatalf("authorized rollback: %d %s", response.Code, response.Body.String())
		}
		id = updated.Deployments[0].ID
	}
}
