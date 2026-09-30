package httpserver

import (
	"context"
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"time"
)

type requestIDKey struct{}
type responseStatus struct {
	http.ResponseWriter
	status int
	bytes  int
}

// WriteHeader 仅记录首次最终响应状态。
func (w *responseStatus) WriteHeader(code int) {
	if code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Write 记录隐式 200 和实际响应字节数。
func (w *responseStatus) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	count, err := w.ResponseWriter.Write(data)
	w.bytes += count
	return count, err
}

// Unwrap 允许 ResponseController 继续操作底层连接。
func (w *responseStatus) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// requestID 返回服务端生成的追踪编号，不信任客户端提交的同名请求头。
func requestID(r *http.Request) string {
	value, _ := r.Context().Value(requestIDKey{}).(string)
	return value
}

// logging 记录状态、耗时和请求编号，避免记录可能含凭据的查询串及请求体。
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := uuid.NewString()
		w.Header().Set("X-Request-ID", id)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
		response := &responseStatus{ResponseWriter: w}
		defer func() {
			status := response.status
			if status == 0 {
				status = http.StatusOK
			}
			level := slog.LevelInfo
			if status >= 400 {
				level = slog.LevelWarn
			}
			if status >= 500 {
				level = slog.LevelError
			}
			slog.Log(r.Context(), level, "http request", "request_id", id, "method", r.Method, "path", r.URL.Path, "status", status, "bytes", response.bytes, "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(response, r)
	})
}
