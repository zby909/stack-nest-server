package httpserver

import (
	"context"
	"net/http"
	"time"

	"stacknest/server/internal/auth"
	"stacknest/server/internal/buildinfo"
	"stacknest/server/internal/doctor"
	"stacknest/server/internal/generated"
)

// diagnosticResult 在只含安全摘要的报告上补充本次检查时间。
type diagnosticResult struct {
	doctor.Report
	CheckedAt  time.Time `json:"checkedAt"`
	DurationMS int64     `json:"durationMs"`
}

// GetVersion 仅向管理员展示后端构建信息，供配套版本排查。
func (s *Server) GetVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !auth.IsAdmin(s.requestUser(r).Role) {
		writeJSON(w, http.StatusForbidden, generated.ErrorResponse{Error: "仅管理员可访问"})
		return
	}
	writeJSON(w, http.StatusOK, buildinfo.Current())
}

// RunDiagnostics 只允许超管手动执行有时限的自检，同一实例至多运行一次。
func (s *Server) RunDiagnostics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.auth == nil || s.requestUser(r).Role != auth.RoleSuperAdmin {
		writeJSON(w, http.StatusForbidden, generated.ErrorResponse{Error: "仅超级管理员可运行平台诊断"})
		return
	}
	if s.config.Inspect == nil {
		writeJSON(w, http.StatusServiceUnavailable, generated.ErrorResponse{Error: "平台诊断暂不可用"})
		return
	}
	if !s.diagnosticMu.TryLock() {
		writeJSON(w, http.StatusConflict, generated.ErrorResponse{Error: "已有诊断正在运行，请稍后重试"})
		return
	}
	defer s.diagnosticMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	start := time.Now()
	report := s.config.Inspect(ctx)
	if ctx.Err() != nil {
		writeJSON(w, http.StatusGatewayTimeout, generated.ErrorResponse{Error: "诊断超时或已取消，请稍后重试"})
		return
	}
	writeJSON(w, http.StatusOK, diagnosticResult{Report: report, CheckedAt: time.Now().UTC(), DurationMS: time.Since(start).Milliseconds()})
}
