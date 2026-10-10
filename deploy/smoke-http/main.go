// smoke-http 是 Docker 应用验收镜像，独立于产品构建和用户应用。
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// main 启动无外部依赖的 HTTP 服务，故障开关仅供更新恢复验收。
func main() {
	if os.Getenv("APP_FAIL") == "1" {
		log.Fatal("intentional smoke startup failure")
	}
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "/app/data"
	}
	version := os.Getenv("APP_VERSION")
	if version == "" {
		version = "v1"
	}
	if value := os.Getenv("APP_SECRET"); value != "" {
		log.Print("smoke configured secret: " + value)
	}
	server := &http.Server{Addr: ":8080", Handler: handler(dir, version), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}

// handler 通过少量端点覆盖页面、应用 API、数据保留、流式和 Cookie 隔离。
func handler(dir, version string) http.Handler {
	mux := http.NewServeMux()
	var mu sync.Mutex
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, "<!doctype html><title>StackNest Docker smoke</title><h1>Docker HTTP %s</h1><p>A small test application.</p>", version)
	})
	mux.HandleFunc("/api/counter", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		file := filepath.Join(dir, "counter.txt")
		raw, _ := os.ReadFile(file)
		count, _ := strconv.Atoi(string(raw))
		if r.Method == "POST" {
			if err := os.MkdirAll(dir, 0700); err != nil {
				http.Error(w, "data unavailable", 500)
				return
			}
			count++
			if err := os.WriteFile(file, []byte(strconv.Itoa(count)), 0600); err != nil {
				http.Error(w, "data write failed", 500)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"count": count, "version": version})
	})
	mux.HandleFunc("GET /api/echo/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(r.RequestURI)) })
	mux.HandleFunc("GET /cookie", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "stacknest_session", Value: "malicious-test-value", Path: "/", Domain: ".localhost"})
		http.SetCookie(w, &http.Cookie{Name: "application", Value: "ok", Path: "/", Domain: ".localhost"})
		_, _ = w.Write([]byte(r.Header.Get("Cookie")))
	})
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			_, _ = fmt.Fprintf(w, "data: %d\n\n", i)
			_ = http.NewResponseController(w).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	})
	mux.HandleFunc("GET /ws", websocket)
	return mux
}
