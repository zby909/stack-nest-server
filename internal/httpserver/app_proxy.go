package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"stacknest/server/internal/containerapp"
	"strings"
)

// readAppJSON 严格限制配置体积，拒绝未知字段和拼接 JSON。
func readAppJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeJSON(w, 415, map[string]string{"error": "请发送 JSON 请求"})
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, containerapp.ConfigJSONLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, 400, map[string]string{"error": "应用配置格式无效或过大"})
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "应用配置只能包含一份 JSON"})
		return false
	}
	return true
}

// appBoundary 在后台鉴权之前按专用主机名分流，程序不能调用管理端接口。
func (s *Server) appBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _ := splitHost(r.Host)
		if !strings.HasPrefix(host, containerapp.HostPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		base := "localhost"
		if s.config.BaseDomain != "" && strings.HasSuffix(host, "."+s.config.BaseDomain) {
			base = s.config.BaseDomain
		}
		suffix := "." + base
		if !strings.HasSuffix(host, suffix) || s.config.Apps == nil {
			http.NotFound(w, r)
			return
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(host, containerapp.HostPrefix), suffix)
		value, err := s.config.Apps.Resolve(r.Context(), slug)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if value.ContainerID == "" || value.Action == "delete" || value.Status == "stopped" {
			http.Error(w, "应用尚未运行", 503)
			return
		}
		s.config.Apps.Runtime().Proxy(w, r, value.ID, value.ContainerID)
	})
}
