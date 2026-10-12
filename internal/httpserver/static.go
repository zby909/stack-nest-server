package httpserver

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"stacknest/server/internal/project"
)

// staticAssetPath 与 Nginx 一致：缺失的常见资源不返回 SPA 页面。
var staticAssetPath = regexp.MustCompile(`(?i)\.(html?|css|[cm]?js|map|json|xml|txt|csv|svg|ico|png|jpe?g|gif|webp|avif|bmp|woff2?|ttf|otf|eot|wasm|webmanifest|pdf|zip|rar|gz|mp4|webm|mp3|ogg|wav)/?$`)

// serveSite 提供正式站点的本地预览，页面路由允许回退到入口文件。
func (s *Server) serveSite(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/sites/")
	if hiddenStaticPath(path) {
		http.NotFound(w, r)
		return
	}
	parts := strings.SplitN(path, "/", 2)
	slug := parts[0]
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	value, err := s.projects.Get(r.Context(), slug)
	if err != nil {
		if !errors.Is(err, project.ErrNotFound) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
		return
	}
	_, markerErr := os.Stat(filepath.Join(filepath.Dir(s.config.SitesDir), "paused", slug))
	if value.Status != "online" || !errors.Is(markerErr, os.ErrNotExist) {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "站点已暂停访问", http.StatusServiceUnavailable)
		return
	}

	relativePath := ""
	if len(parts) == 2 {
		relativePath = parts[1]
	}
	if relativePath == "" {
		relativePath = "index.html"
	}

	root := filepath.Join(s.config.SitesDir, slug)
	target, ok := safeJoin(root, relativePath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		http.ServeFile(w, r, target)
		return
	}

	if staticAssetPath.MatchString(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(root, "index.html"))
}

func (s *Server) serveWeb(w http.ResponseWriter, r *http.Request) {
	if hiddenStaticPath(r.URL.Path) || strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/sites/") {
		http.NotFound(w, r)
		return
	}

	relativePath := strings.TrimLeft(r.URL.Path, "/\\")
	if relativePath == "" {
		relativePath = "index.html"
	}

	target, ok := safeJoin(s.config.WebDir, relativePath)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		http.ServeFile(w, r, target)
		return
	}

	if staticAssetPath.MatchString(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	index := filepath.Join(s.config.WebDir, "index.html")
	if _, err := os.Stat(index); err == nil {
		http.ServeFile(w, r, index)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "StackNest API is running. Build the frontend with pnpm build.",
	})
}

// hiddenStaticPath 统一拒绝隐藏文件和目录，普通含点文件名不受影响。
func hiddenStaticPath(path string) bool {
	for _, part := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// safeJoin 将 URL 相对路径限制在指定根目录内。
func safeJoin(root, relative string) (string, bool) {
	cleanRoot := filepath.Clean(root)
	target := filepath.Clean(filepath.Join(cleanRoot, filepath.FromSlash(relative)))
	if target == cleanRoot {
		return target, true
	}
	if !strings.HasPrefix(target, cleanRoot+string(os.PathSeparator)) {
		return "", false
	}
	return target, true
}
