package httpserver

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"stacknest/server/internal/project"
)

func (s *Server) serveSite(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/sites/")
	parts := strings.SplitN(path, "/", 2)
	slug := parts[0]
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	if _, err := s.projects.Get(r.Context(), slug); err != nil {
		if !errors.Is(err, project.ErrNotFound) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
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

	http.ServeFile(w, r, filepath.Join(root, "index.html"))
}

func (s *Server) serveWeb(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/sites/") {
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

	index := filepath.Join(s.config.WebDir, "index.html")
	if _, err := os.Stat(index); err == nil {
		http.ServeFile(w, r, index)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "StackNest API is running. Build the frontend with pnpm build.",
	})
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
