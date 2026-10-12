package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/egress"
	"stacknest/server/internal/generated"
)

// NetworkControl 是后台可访问的最小宿主配置能力，不提供任意规则或命令。
type NetworkControl interface {
	Get(ctx context.Context) (egress.View, error)
	Update(ctx context.Context, input egress.Update) (egress.View, error)
}

// networkUser 对未配置认证的测试入口也执行超管检查。
func (s *Server) networkUser(w http.ResponseWriter, r *http.Request) bool {
	if s.auth == nil || s.requestUser(r).Role != auth.RoleSuperAdmin {
		writeJSON(w, http.StatusForbidden, generated.ErrorResponse{Error: "仅超级管理员可配置容器网络"})
		return false
	}
	return true
}

// GetNetwork 查询实际网卡、保存的配置和当前有效规则。
func (s *Server) GetNetwork(w http.ResponseWriter, r *http.Request) {
	if !s.networkUser(w, r) {
		return
	}
	if s.config.Network == nil {
		writeJSON(w, 503, generated.ErrorResponse{Error: "尚未启用宿主网络配置服务"})
		return
	}
	view, err := s.config.Network.Get(r.Context())
	if err != nil {
		writeJSON(w, 503, generated.ErrorResponse{Error: "无法连接宿主网络服务，请检查 stacknest-egress 服务及控制目录挂载"})
		return
	}
	writeJSON(w, 200, view)
}

// UpdateNetwork 严格校验请求及版本，只有宿主确认规则生效后才返回成功。
func (s *Server) UpdateNetwork(w http.ResponseWriter, r *http.Request) {
	if !s.networkUser(w, r) {
		return
	}
	if s.config.Network == nil {
		writeJSON(w, 503, generated.ErrorResponse{Error: "尚未启用宿主网络配置服务"})
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeJSON(w, 415, generated.ErrorResponse{Error: "请发送 JSON 请求"})
		return
	}
	var input egress.Update
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil {
		writeJSON(w, 400, generated.ErrorResponse{Error: "配置请求无效"})
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		writeJSON(w, 400, generated.ErrorResponse{Error: "配置包含多余内容"})
		return
	}
	normalized, err := egress.Normalize(input.Config)
	if err != nil {
		writeJSON(w, 400, generated.ErrorResponse{Error: err.Error()})
		return
	}
	input.Config = normalized
	start := time.Now()
	view, err := s.config.Network.Update(r.Context(), input)
	if s.config.Activity != nil {
		status, message := "succeeded", "已更新容器网络访问策略"
		if err != nil {
			status, message = "failed", "容器网络策略更新失败"
		}
		user := s.requestUser(r)
		event := activity.Event{ID: uuid.NewString(), CreatedAt: time.Now().UTC(), Action: "network", Actor: user.Email, ActorID: user.ID, Status: status, Message: message, DurationMS: time.Since(start).Milliseconds()}
		if logErr := s.config.Activity.Append(event); logErr != nil {
			slog.Error("record network update", "error", logErr)
		}
	}
	if err != nil {
		code := 400
		if errors.Is(err, egress.ErrConflict) {
			code = 409
		} else if errors.Is(err, egress.ErrUnavailable) {
			code = 503
		}
		writeJSON(w, code, generated.ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, 200, view)
}
