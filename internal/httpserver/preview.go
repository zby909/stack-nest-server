package httpserver

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"stacknest/server/internal/project"
)

// previewSlug 从本机保留域名提取项目，确保每个预览与后台分别占用一个来源。
func previewSlug(host string) string {
	host, _ = splitHost(host)
	if !strings.HasPrefix(host, "stack-nest-") || !strings.HasSuffix(host, ".localhost") {
		return ""
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(host, "stack-nest-"), ".localhost")
	if !project.ValidSlug(slug) {
		return ""
	}
	return slug
}

// splitHost 统一处理端口、大小写及 IPv6 回环地址。
func splitHost(value string) (string, string) {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return strings.ToLower(value), ""
	}
	return strings.ToLower(host), port
}

// previewBoundary 拒绝管理来源执行上传内容，也拒绝预览来源访问任何管理接口。
func (s *Server) previewBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := previewSlug(r.Host)
		if slug != "" {
			prefix := "/sites/" + slug + "/"
			if (r.Method != http.MethodGet && r.Method != http.MethodHead) || strings.HasPrefix(r.URL.Path, "/api/") || (strings.HasPrefix(r.URL.Path, "/sites/") && !strings.HasPrefix(r.URL.Path, prefix)) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if !strings.HasPrefix(r.URL.Path, "/sites/") {
				cloned := r.Clone(r.Context())
				cloned.URL = new(url.URL)
				*cloned.URL = *r.URL
				cloned.URL.Path = prefix + strings.TrimPrefix(r.URL.Path, "/")
				cloned.URL.RawPath = ""
				r = cloned
			}
			s.serveSite(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/sites/") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// localSiteURL 为 Vite、Go 和 SSH 入口生成不同于管理主机的预览地址。
func localSiteURL(r *http.Request, slug string) string {
	_, port := splitHost(r.Host)
	host := "stack-nest-" + slug + ".localhost"
	if port != "" {
		host = net.JoinHostPort(host, port)
	}
	return fmt.Sprintf("%s://%s/", requestScheme(r), host)
}

// isLoopbackHost 判断是否应优先使用本机预览，而非被备案限制的公网入口。
func isLoopbackHost(value string) bool {
	host, _ := splitHost(value)
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
