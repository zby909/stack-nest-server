package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"stacknest/server/internal/activity"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/generated"
	"stacknest/server/internal/storage"
)

// newActivity 初始化只包含业务信息的操作记录。
func newActivity(r *http.Request, action string) *activity.Event {
	user, _ := r.Context().Value(authContextKey{}).(auth.User)
	event := &activity.Event{ID: requestID(r), CreatedAt: time.Now().UTC(), Action: action, Actor: user.Email, ActorID: user.ID, Status: "failed", Message: "请求未完成"}
	if grant, ok := deploymentGrant(r); ok {
		event.TokenName = grant.Name
		event.TokenID = grant.ID
	}
	return event
}

// finishActivity 即使客户端断开也保存结果；日志写入失败会记录到进程日志。
func (s *Server) finishActivity(r *http.Request, event *activity.Event) {
	event.DurationMS = time.Since(event.CreatedAt).Milliseconds()
	slog.InfoContext(r.Context(), "project operation", "request_id", event.ID, "actor", event.Actor, "action", event.Action, "slug", event.Slug, "result", event.Status, "archive_bytes", event.ArchiveBytes, "expanded_bytes", event.ExpandedBytes, "message", event.Message)
	if s.config.Activity != nil {
		if err := s.config.Activity.Append(*event); err != nil {
			slog.Error("persist activity log", "request_id", event.ID, "error", err)
		}
	}
}

// GetStorage 向所有用户提供上传策略，仅向管理员展示磁盘容量。
func (s *Server) GetStorage(w http.ResponseWriter, r *http.Request) {
	usage, err := s.config.Storage.Usage()
	if err != nil {
		slog.ErrorContext(r.Context(), "read storage usage", "request_id", requestID(r), "error", err)
		writeJSON(w, http.StatusServiceUnavailable, generated.ErrorResponse{Error: uploadStorageReason(s.requestUser(r), true)})
		return
	}
	allowed := storage.CheckUsage(usage, storage.UploadBudget) == nil
	user := s.requestUser(r)
	reason := ""
	if !allowed {
		reason = uploadStorageReason(user, false)
	}
	response := map[string]any{
		"uploadAllowed": allowed, "reason": reason,
		"maxUploadBytes": s.config.MaxUploadSize, "maxExpandedBytes": storage.MaxExpandedBytes,
		"maxFiles": storage.MaxFiles,
	}
	if auth.IsAdmin(user.Role) {
		response["totalBytes"] = usage.TotalBytes
		response["freeBytes"] = usage.FreeBytes
		response["usedPercent"] = usage.UsedPercent
		response["maxUsedPercent"] = storage.MaxUsedPercent
		response["reservedUploadBytes"] = storage.UploadBudget
	}
	writeJSON(w, http.StatusOK, response)
}

// uploadStorageReason 统一上传与容量接口的提示，普通用户只获知原因和处理方式。
func uploadStorageReason(user auth.User, unavailable bool) string {
	if unavailable {
		if auth.IsAdmin(user.Role) {
			return "暂时无法检查磁盘空间，已暂停上传和更新，请检查服务后重试"
		}
		return "暂时无法检查上传条件，已暂停上传和更新，请稍后重新检查"
	}
	if auth.IsAdmin(user.Role) {
		return "磁盘使用率已达到 90%，或剩余空间不足以接收和解压文件；已暂停上传和更新，请清理后重试"
	}
	return "服务器存储空间不足，已暂停上传和更新；请联系管理员清理后重新检查"
}

// GetSystem 仅向管理员返回主机 CPU、内存、运行时间及数据卷容量。
func (s *Server) GetSystem(w http.ResponseWriter, r *http.Request) {
	if !auth.IsAdmin(s.requestUser(r).Role) {
		writeJSON(w, http.StatusForbidden, generated.ErrorResponse{Error: "仅管理员可访问"})
		return
	}
	system, err := s.config.ReadSystem(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "read system usage", "request_id", requestID(r), "error", err)
		writeJSON(w, http.StatusServiceUnavailable, generated.ErrorResponse{Error: "暂时无法读取服务器状态"})
		return
	}
	disk, err := s.config.Storage.Usage()
	if err != nil {
		slog.ErrorContext(r.Context(), "read storage usage", "request_id", requestID(r), "error", err)
		writeJSON(w, http.StatusServiceUnavailable, generated.ErrorResponse{Error: "暂时无法读取服务器状态"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cpuPercent": system.CPUPercent, "cpuCores": system.CPUCores,
		"memoryTotalBytes": system.MemoryTotalBytes, "memoryAvailableBytes": system.MemoryAvailableBytes,
		"memoryUsedPercent": system.MemoryUsedPercent, "uptimeSeconds": system.UptimeSeconds,
		"diskTotalBytes": disk.TotalBytes, "diskFreeBytes": disk.FreeBytes,
		"diskUsedPercent": disk.UsedPercent,
	})
}
