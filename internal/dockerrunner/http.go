package dockerrunner

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// proxy 对全部 HTTP 路径转发到已确认容器，应用自己的 /api/ 不会访问后台。
func (r *Runner) proxy(w http.ResponseWriter, req *http.Request) {
	id, cid := req.PathValue("id"), req.PathValue("container")
	value, err := r.owned(req.Context(), id, cid)
	if err != nil || !value.State.Running {
		http.Error(w, "应用暂时不可用", 503)
		return
	}
	address, err := r.address(value, id)
	if err != nil {
		http.Error(w, "应用网络未就绪", 503)
		return
	}
	transport := &http.Transport{DisableKeepAlives: true, ResponseHeaderTimeout: 60 * time.Second, DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		current, err := r.owned(ctx, id, cid)
		if err != nil || !current.State.Running {
			return nil, errors.New("application stopped")
		}
		actual, err := r.address(current, id)
		if err != nil || actual != address || addr != address {
			return nil, errors.New("application target changed")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	}}
	defer transport.CloseIdleConnections()
	proxy := &httputil.ReverseProxy{Transport: transport, FlushInterval: -1, Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(&url.URL{Scheme: "http", Host: address})
		p.Out.URL.Path = "/" + req.PathValue("path")
		p.Out.URL.RawPath = strings.TrimPrefix(req.URL.EscapedPath(), "/proxy/"+id+"/"+cid)
		p.Out.Host = req.Host
		// 后台会话永不发送至用户程序。
		p.Out.Header.Del("Cookie")
		for _, cookie := range req.Cookies() {
			if cookie.Name != "stacknest_session" && cookie.Name != "__Host-stacknest_session" {
				p.Out.AddCookie(cookie)
			}
		}
		p.SetXForwarded()
		p.Out.Header.Set("X-Forwarded-For", req.Header.Get("X-Forwarded-For"))
		p.Out.Header.Set("X-Forwarded-Proto", req.Header.Get("X-Forwarded-Proto"))
		p.Out.Header.Set("X-Forwarded-Host", req.Host)
	}, ModifyResponse: func(response *http.Response) error {
		cookies := response.Cookies()
		response.Header.Del("Set-Cookie")
		for _, cookie := range cookies {
			if cookie.Name == "stacknest_session" || cookie.Name == "__Host-stacknest_session" {
				continue
			}
			cookie.Domain = ""
			response.Header.Add("Set-Cookie", cookie.String())
		}
		return nil
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { http.Error(w, "应用连接失败", 502) }}
	proxy.ServeHTTP(w, req)
}

// logs 将 Docker 多路复用日志解帧，最多返回最近 200 行及 256 KiB。
func (r *Runner) logs(w http.ResponseWriter, req *http.Request) {
	id, cid := req.PathValue("id"), req.PathValue("container")
	if _, err := r.owned(req.Context(), id, cid); err != nil {
		respond(w, 404, map[string]string{"error": "应用容器不存在"})
		return
	}
	response, err := r.engine.request(req.Context(), "GET", "/containers/"+url.PathEscape(cid)+"/logs?stdout=1&stderr=1&timestamps=1&tail=200", nil, nil)
	if err != nil {
		respond(w, 503, map[string]string{"error": "日志暂时不可用"})
		return
	}
	defer response.Body.Close()
	text, err := readLogs(response.Body)
	if err != nil {
		respond(w, 503, map[string]string{"error": "日志读取失败或超出限制"})
		return
	}
	respond(w, 200, map[string]string{"text": text})
}

// readLogs 拒绝超长日志帧，避免内存分配受程序输出控制。
func readLogs(source io.Reader) (string, error) {
	var text strings.Builder
	for text.Len() < 256<<10 {
		var header [8]byte
		_, err := io.ReadFull(source, header[:])
		if err == io.EOF {
			return text.String(), nil
		}
		if err != nil {
			return "", err
		}
		length := int(binary.BigEndian.Uint32(header[4:]))
		if length > 256<<10 || length < 0 {
			return "", errors.New("log frame too large")
		}
		raw := make([]byte, length)
		if _, err = io.ReadFull(source, raw); err != nil {
			return "", err
		}
		remaining := (256 << 10) - text.Len()
		if length > remaining {
			raw = raw[:remaining]
		}
		text.Write(raw)
	}
	return text.String() + "\n[日志已截断]", nil
}

// maintenance 冻结期间停止所有本实例运行应用，再按原状态恢复。
func (r *Runner) maintenance(w http.ResponseWriter, req *http.Request) {
	var input struct {
		Frozen bool `json:"frozen"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1024)).Decode(&input); err != nil {
		http.Error(w, "无效参数", 400)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	file := filepath.Join(r.dataDir, "app-maintenance.json")
	if input.Frozen {
		values, err := r.containers(req.Context(), "")
		if err != nil {
			http.Error(w, "冻结失败", 503)
			return
		}
		ids := map[string]string{}
		if r.frozen {
			raw, readErr := os.ReadFile(file)
			if readErr != nil || json.Unmarshal(raw, &ids) != nil {
				http.Error(w, "冻结标记无效", 503)
				return
			}
		}
		for _, value := range values {
			if value.State == "running" {
				if _, err = r.owned(req.Context(), value.Labels[appLabel], value.ID); err != nil {
					http.Error(w, "容器归属异常", 503)
					return
				}
				ids[value.ID] = value.Labels[appLabel]
			}
		}
		raw, _ := json.Marshal(ids)
		if err = os.WriteFile(file, raw, 0600); err != nil {
			http.Error(w, "冻结标记写入失败", 503)
			return
		}
		r.frozen = true
		for cid, id := range ids {
			value, inspectErr := r.owned(req.Context(), id, cid)
			if errors.Is(inspectErr, errMissing) {
				continue
			}
			if inspectErr != nil {
				http.Error(w, "冻结归属检查失败", 503)
				return
			}
			if !value.State.Running {
				continue
			}
			if err = r.stop(req.Context(), cid); err != nil {
				http.Error(w, "应用停止失败", 503)
				return
			}
		}
	} else {
		raw, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			r.frozen = false
			respond(w, 200, map[string]bool{"frozen": false})
			return
		}
		ids := map[string]string{}
		if err != nil || json.Unmarshal(raw, &ids) != nil {
			http.Error(w, "冻结标记无效", 503)
			return
		}
		for cid, id := range ids {
			value, err := r.owned(req.Context(), id, cid)
			if errors.Is(err, errMissing) {
				continue
			}
			if err != nil {
				http.Error(w, "恢复归属检查失败", 503)
				return
			}
			if !value.State.Running {
				if err = r.start(req.Context(), cid); err != nil {
					http.Error(w, "应用恢复失败", 503)
					return
				}
			}
		}
		if err = os.Remove(file); err != nil {
			http.Error(w, "冻结标记清理失败", 503)
			return
		}
		r.frozen = false
	}
	respond(w, 200, map[string]bool{"frozen": input.Frozen})
}
