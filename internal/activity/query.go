package activity

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrInvalidQuery = errors.New("invalid activity query")

// Query 将身份限制和用户筛选分开，分页与总数均只包含可见记录。
type Query struct {
	UserID                               string
	IsAdmin                              bool
	Page, PageSize                       int
	Slug, Action, Status, Actor, Keyword string
	From, To                             time.Time
}

// Page 是当前保留日志的查询结果，轮转后被清理的记录不再计入。
type Page struct {
	Events   []Event `json:"events"`
	Total    int     `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"pageSize"`
}

// Validate 拒绝越界分页、无效枚举和反向时间区间。
func (q Query) Validate() error {
	if q.Page < 1 || q.Page > 100000 || q.PageSize < 1 || q.PageSize > 100 || len([]rune(q.Slug)) > 52 || len(q.Actor) > 254 || len([]rune(q.Keyword)) > 256 {
		return ErrInvalidQuery
	}
	switch q.Action {
	case "", "upload", "update", "rollback", "delete", "edit", "export", "pause", "resume", "draft", "publish", "discard", "app-deploy", "app-start", "app-stop", "app-restart", "app-delete", "network", "app-limits", "user-add", "user-role":
	default:
		return ErrInvalidQuery
	}
	if q.Status != "" && q.Status != "succeeded" && q.Status != "failed" {
		return ErrInvalidQuery
	}
	if !q.From.IsZero() && !q.To.IsZero() && !q.From.Before(q.To) {
		return ErrInvalidQuery
	}
	return nil
}

// Query 先按身份和条件过滤全部保留日志，再计算总数及当前页。
func (s *Store) Query(ctx context.Context, query Query) (Page, error) {
	result := Page{Events: []Event{}, Page: query.Page, PageSize: query.PageSize}
	if err := query.Validate(); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.readEvents(ctx)
	if err != nil {
		return result, err
	}
	keyword := strings.ToLower(strings.TrimSpace(query.Keyword))
	actor := strings.TrimSpace(query.Actor)
	start := (query.Page - 1) * query.PageSize
	for _, event := range events {
		if !query.IsAdmin && (query.UserID == "" || event.ActorID != query.UserID && event.ProjectOwnerID != query.UserID) {
			continue
		}
		if query.Slug != "" && event.Slug != query.Slug || query.Action != "" && event.Action != query.Action || query.Status != "" && event.Status != query.Status {
			continue
		}
		if !query.From.IsZero() && event.CreatedAt.Before(query.From) || !query.To.IsZero() && !event.CreatedAt.Before(query.To) {
			continue
		}
		if actor != "" && !strings.EqualFold(event.Actor, actor) {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(strings.Join([]string{event.Slug, event.FileName, event.Actor, event.ID, event.Message}, "\n")), keyword) {
			continue
		}
		if result.Total >= start && len(result.Events) < query.PageSize {
			result.Events = append(result.Events, event)
		}
		result.Total++
	}
	return result, nil
}
