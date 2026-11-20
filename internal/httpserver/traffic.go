package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"stacknest/server/internal/project"
	"stacknest/server/internal/traffic"
)

// refreshTraffic 在新资源对外返回前刷新路由清单，后台的定期同步也能修复中断。
func (s *Server) refreshTraffic(ctx context.Context) {
	if reg := s.config.Traffic; reg != nil {
		if err := reg.Refresh(ctx); err != nil {
			slog.WarnContext(ctx, "刷新访问统计域名失败", "error", err)
		}
	}
}

// projectTraffic 对项目逐一鉴权，未知和其他成员的项目使用相同响应。
func (s *Server) projectTraffic(w http.ResponseWriter, r *http.Request) {
	p, err := s.projects.Get(r.Context(), r.PathValue("slug"))
	if err == nil && !canManageProject(s.requestUser(r), p) {
		err = project.ErrNotFound
	}
	if err != nil {
		s.writeProjectError(w, r, err)
		return
	}
	s.sendTraffic(w, "site:"+p.ID, p.CreatedAt)
}

// appTraffic 按当前所有权和应用主键读取，应用重建后不会继承同名旧应用的记录。
func (s *Server) appTraffic(w http.ResponseWriter, r *http.Request) {
	if !s.appsReady(w) {
		return
	}
	a, err := s.config.Apps.Get(r.Context(), r.PathValue("id"), s.appActor(r))
	if err != nil {
		s.writeAppError(w, r, err)
		return
	}
	s.sendTraffic(w, "app:"+a.ID, a.CreatedAt)
}

// sendTraffic 返回有序的 UTC 日期，禁用和没有请求是两种独立状态。
func (s *Server) sendTraffic(w http.ResponseWriter, id string, createdAt time.Time) {
	value, err := traffic.Read(filepath.Dir(s.config.SitesDir), id, time.Now())
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "暂时无法读取访问统计"})
		return
	}
	if !value.Since.IsZero() && createdAt.After(value.Since) {
		value.Since = createdAt.UTC()
	}
	writeJSON(w, 200, value)
}
