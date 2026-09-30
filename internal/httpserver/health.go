package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"stacknest/server/internal/generated"
)

const databaseHealthTimeout = 2 * time.Second

// GetHealth 用有时限的数据库探测报告就绪状态，公开响应不包含连接信息或内部错误。
func (s *Server) GetHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), databaseHealthTimeout)
	defer cancel()
	err := errors.New("database health check is not configured")
	if s.config.CheckDatabase != nil {
		err = s.config.CheckDatabase(ctx)
	}
	if err != nil {
		slog.WarnContext(r.Context(), "database health check failed", "request_id", requestID(r), "error", err)
		writeJSON(w, http.StatusServiceUnavailable, generated.HealthResponse{Status: generated.Unavailable})
		return
	}
	writeJSON(w, http.StatusOK, generated.HealthResponse{Status: generated.Ok})
}
