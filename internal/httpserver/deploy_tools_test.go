package httpserver

import (
	"archive/zip"
	"bytes"
	"net/http/httptest"
	"testing"

	"stacknest/server/internal/auth"
)

// TestDeploySkillDownload 允许已登录的普通用户下载安装包，但不把部署 Token 当作后台会话。
func TestDeploySkillDownload(t *testing.T) {
	server := platformServer(t, auth.RoleUser, nil)
	for _, loggedIn := range []bool{false, true} {
		req := platformRequest("GET", "/api/deploy-tools/skill", "http://localhost", loggedIn)
		if !loggedIn {
			req.Header.Set("Authorization", "Bearer snd_not_a_management_session")
		}
		res := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(res, req)
		if res.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("download or authentication response is cacheable")
		}
		if !loggedIn {
			if res.Code != 401 {
				t.Fatalf("download without management session: %d", res.Code)
			}
			continue
		}
		if res.Code != 200 || res.Header().Get("Content-Type") != "application/zip" ||
			res.Header().Get("Content-Disposition") != `attachment; filename="stacknest-deploy-skill.zip"` {
			t.Fatalf("invalid download response: %d %v", res.Code, res.Header())
		}
		if _, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len())); err != nil {
			t.Fatal(err)
		}
	}
}
