package httpserver

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"stacknest/server/internal/project"
)

// versionURL 使用独立单级子域名，与管理端和正式站点隔离并兼容通配证书。
func (s *Server) versionURL(r *http.Request, id string) string {
	name := "stack-nest--" + strings.TrimPrefix(id, "dep-")
	base, port := s.config.BaseDomain, ""
	if base == "" || isLoopbackHost(r.Host) {
		base = "localhost"
		_, port = splitHost(r.Host)
	}
	host := name + "." + base
	if port != "" {
		host = net.JoinHostPort(host, port)
	}
	return fmt.Sprintf("%s://%s/", requestScheme(r), host)
}

// isVersionHost 保留整个版本命名空间，非法编号也不能落入管理页面。
func (s *Server) isVersionHost(value string) bool {
	host, _ := splitHost(value)
	if !strings.HasPrefix(host, "stack-nest--") {
		return false
	}
	return strings.HasSuffix(host, ".localhost") || s.config.BaseDomain != "" && strings.HasSuffix(host, "."+s.config.BaseDomain)
}

// serveVersion 通过已保留版本打开受限目录，只提供静态内容且永不访问后台接口。
func (s *Server) serveVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	host, _ := splitHost(r.Host)
	label, _, _ := strings.Cut(host, ".")
	id := strings.TrimPrefix(label, "stack-nest--")
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "api/") || strings.HasPrefix(name, "sites/") || name == "openapi.yaml" {
		http.NotFound(w, r)
		return
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			http.NotFound(w, r)
			return
		}
	}
	root, err := s.projects.OpenPreview(r.Context(), "dep-"+id)
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "预览暂时不可用", 503)
		}
		return
	}
	defer root.Close()
	if name == "" {
		name = "index.html"
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		if staticAssetPath.MatchString(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}
	file, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}
