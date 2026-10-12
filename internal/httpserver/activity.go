package httpserver

import (
	"log/slog"
	"net/http"
	"strings"

	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
)

// GetActivity 在服务端保留的日志中筛选并分页，不将他人记录计入普通用户的总数。
func (s *Server) GetActivity(w http.ResponseWriter, r *http.Request, params generated.GetActivityParams) {
	user := s.requestUser(r)
	query := activity.Query{UserID: user.ID, IsAdmin: auth.IsAdmin(user.Role), Page: 1, PageSize: 50}
	if params.Page != nil {
		query.Page = *params.Page
	}
	if params.PageSize != nil {
		query.PageSize = *params.PageSize
	}
	if params.Slug != nil {
		query.Slug = strings.TrimSpace(*params.Slug)
	}
	if params.Action != nil {
		query.Action = *params.Action
	}
	if params.Status != nil {
		query.Status = *params.Status
	}
	if params.Actor != nil {
		query.Actor = strings.TrimSpace(*params.Actor)
	}
	if params.Q != nil {
		query.Keyword = strings.TrimSpace(*params.Q)
	}
	if params.From != nil {
		query.From = *params.From
	}
	if params.To != nil {
		query.To = *params.To
	}
	if err := query.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, generated.ErrorResponse{Error: "日志筛选参数或时间范围无效"})
		return
	}
	result := activity.Page{Events: []activity.Event{}, Page: query.Page, PageSize: query.PageSize}
	if s.config.Activity != nil {
		var err error
		result, err = s.config.Activity.Query(r.Context(), query)
		if err != nil {
			slog.ErrorContext(r.Context(), "read activity log", "request_id", requestID(r), "error", err)
			writeJSON(w, http.StatusInternalServerError, generated.ErrorResponse{Error: "操作日志暂时无法读取"})
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
