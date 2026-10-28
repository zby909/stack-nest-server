package httpserver

import (
	"bytes"
	"net/http"
	"time"

	"stacknest/server/internal/generated"
	"stacknest/server/skills"
)

// DownloadDeploySkill 提供随平台编译的通用 Skill，登录校验由管理接口边界负责。
func (s *Server) DownloadDeploySkill(w http.ResponseWriter, r *http.Request) {
	archive, err := skills.Archive()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, generated.ErrorResponse{Error: "Skill 下载包暂时不可用"})
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="stacknest-deploy-skill.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "stacknest-deploy-skill.zip", time.Time{}, bytes.NewReader(archive))
}
