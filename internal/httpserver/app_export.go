package httpserver

import (
	"net/http"
	"stacknest/server/internal/backup"
)

// appExport 对摘要与实际下载分别核对当前应用权限，直接向浏览器流式传送快照。
func (s *Server) appExport(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w) {
		return
	}
	app, err := s.config.Apps.Get(r.Context(), r.PathValue("id"), s.appActor(r))
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	value := backup.AppExport{State: "not_configured"}
	if app.DataPath != "" {
		value, err = backup.LookupAppExport(s.config.BackupStatusFile, app.ID)
	}
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "暂时无法读取备份索引"})
		return
	}
	if r.URL.Query().Get("download") == "" {
		writeJSON(w, 200, value)
		return
	}
	if value.State != "ready" || r.URL.Query().Get("download") != value.Snapshot {
		writeJSON(w, 409, map[string]string{"error": "备份已更新或暂不可用，请刷新后重新下载"})
		return
	}
	f, err := value.Open()
	if err != nil {
		writeJSON(w, 409, map[string]string{"error": "备份正在轮换，请刷新后重新下载"})
		return
	}
	defer f.Close()
	name := app.Slug + "-data-" + value.CreatedAt.UTC().Format("20060102T150405Z") + ".tar.gz"
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, name, *value.CreatedAt, f)
}
