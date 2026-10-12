package egress

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// writeResponse 输出有界的 JSON 响应，不暴露宿主命令输出。
func writeResponse(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

// handler 仅暴露读取和更新结构化配置两个控制操作。
func (m *Manager) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) { writeResponse(w, 200, m.snapshot()) })
	mux.HandleFunc("PUT /config", func(w http.ResponseWriter, r *http.Request) {
		var input Update
		if err := decode(http.MaxBytesReader(w, r.Body, 64<<10), &input); err != nil {
			writeResponse(w, 400, map[string]string{"error": err.Error()})
			return
		}
		view, err := m.update(r.Context(), input)
		if err != nil {
			code := 400
			if errors.Is(err, ErrConflict) {
				code = 409
			} else if errors.Is(err, ErrUnavailable) {
				code = 503
			}
			writeResponse(w, code, map[string]string{"error": err.Error()})
			return
		}
		writeResponse(w, 200, view)
	})
	return mux
}

// notify 告知 systemd 首批规则已加载，再放行 Docker 开机检查。
func notify() error {
	address := os.Getenv("NOTIFY_SOCKET")
	if address == "" {
		return nil
	}
	if address[0] == '@' {
		address = "\x00" + address[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: address, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte("READY=1"))
	return err
}

// watch 启动权限受限的 Unix 控制端点并持续核验规则，退出时保留已有表。
func watch(ctx context.Context, paths Paths) error {
	m := &Manager{paths: paths}
	if err := m.synchronize(ctx); err != nil {
		return err
	}
	socket := filepath.Join(paths.State, "control.sock")
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("控制路径不是 Unix socket")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	if err = os.Chown(socket, 10001, -1); err != nil {
		return err
	}
	server := &http.Server{Handler: m.handler(), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		task, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(task)
	}()
	if err = notify(); err != nil {
		return err
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ticker.C:
			if err = m.synchronize(ctx); err != nil {
				slog.Error("egress refresh failed; previous rules retained", "error", err)
			}
		}
	}
}
