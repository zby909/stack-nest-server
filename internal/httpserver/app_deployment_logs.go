package httpserver

import (
	"net/http"
	"stacknest/server/internal/generated"
)

// GetAppDeploymentLogs 复用应用所有权和代次校验，容器已被删除也能查看脱敏失败快照。
func (s *Server) GetAppDeploymentLogs(w http.ResponseWriter, r *http.Request, id string, params generated.GetAppDeploymentLogsParams) {
	if !s.appsReady(w) {
		return
	}
	value, err := s.config.Apps.Get(r.Context(), id, s.appActor(r))
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	if params.Generation != nil {
		generation := *params.Generation
		if generation < 1 {
			writeJSON(w, 400, map[string]string{"error": "任务代次无效"})
			return
		}
		if generation != value.Generation {
			writeJSON(w, 409, map[string]string{"error": "部署任务已更新，请刷新后重试"})
			return
		}
	}
	text, err := s.config.Apps.DeploymentLogs(r.Context(), value)
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"text": text})
}
