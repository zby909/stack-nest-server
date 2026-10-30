package containerapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"
)

// Client 使用独立 Unix socket，后台无需持有 Docker daemon 的 socket。
type Client struct {
	transport *http.Transport
	client    *http.Client
}

// NewClient 未配置路径时操作返回不可用，静态站点功能保持正常。
func NewClient(socket string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if socket == "" {
			return nil, ErrUnavailable
		}
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 16}
	return &Client{transport: transport, client: &http.Client{Transport: transport}}
}

// call 处理运行器的安全错误响应，不输出请求中的敏感配置。
func (c *Client) call(ctx context.Context, method, path string, input, target any) error {
	var reader io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://runner"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		return ErrNotFound
	}
	if response.StatusCode >= 400 {
		var failure struct {
			Error  string `json:"error"`
			Result Result `json:"result"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure) != nil {
			return ErrUnavailable
		}
		if value, ok := target.(*Result); ok {
			*value = failure.Result
		}
		if failure.Error == "" {
			return ErrUnavailable
		}
		return errors.New(failure.Error)
	}
	if target != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 300<<10)).Decode(target)
	}
	return nil
}

// Health 使用短超时检查服务，禁止浏览器请求无限等待。
func (c *Client) Health(ctx context.Context) error {
	task, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.call(task, "GET", "/health", nil, nil)
}

// Capacity 读取宿主核对后的预算上限，不开放任意 Docker 信息查询。
func (c *Client) Capacity(ctx context.Context) (Capacity, error) {
	task, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var value Capacity
	err := c.call(task, "GET", "/capacity", nil, &value)
	return value, err
}

// Progress 读取对应操作代次的瞬时进度，浏览器断开不影响任务执行。
func (c *Client) Progress(ctx context.Context, id string, generation int64) (Progress, error) {
	task, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var value Progress
	err := c.call(task, "GET", "/progress/"+id+"/"+strconv.FormatInt(generation, 10), nil, &value)
	return value, err
}

// Operate 执行有时限的后台生命周期操作。
func (c *Client) Operate(ctx context.Context, op Operation) (Result, error) {
	var result Result
	err := c.call(ctx, "POST", "/operation", op, &result)
	if err != nil && err.Error() == ErrDeployCanceled.Error() {
		err = ErrDeployCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = ErrOperationTimeout
	}
	return result, err
}

// State 获取一个应用容器的有界状态。
func (c *Client) State(ctx context.Context, id, cid string) (Result, error) {
	if cid == "" {
		return Result{State: "missing"}, nil
	}
	task, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var value Result
	err := c.call(task, "GET", "/state/"+id+"/"+cid, nil, &value)
	return value, err
}

// Logs 获取有界文本日志。
func (c *Client) Logs(ctx context.Context, id, cid string) (string, error) {
	if cid == "" {
		return "", ErrNotFound
	}
	task, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var value struct {
		Text string `json:"text"`
	}
	err := c.call(task, "GET", "/logs/"+id+"/"+cid, nil, &value)
	return value.Text, err
}

// Proxy 转发到运行器私有端点，支持流式响应与 WebSocket。
func (c *Client) Proxy(w http.ResponseWriter, r *http.Request, id, cid string) {
	proxy := &httputil.ReverseProxy{Transport: c.transport, FlushInterval: -1, Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(&url.URL{Scheme: "http", Host: "runner"})
		p.Out.URL.Path = "/proxy/" + id + "/" + cid + r.URL.Path
		p.Out.URL.RawPath = "/proxy/" + id + "/" + cid + r.URL.EscapedPath()
		p.Out.Host = r.Host
		p.SetXForwarded()
		if ip := net.ParseIP(r.Header.Get("X-Real-IP")); ip != nil {
			p.Out.Header.Set("X-Forwarded-For", ip.String())
		}
		p.Out.Header.Set("X-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"))
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "应用运行器连接失败", 502)
	}}
	proxy.ServeHTTP(w, r)
}

// Maintenance 为部署备份提供只冻结本实例应用的受限入口。
func (c *Client) Maintenance(ctx context.Context, frozen bool) error {
	return c.call(ctx, "POST", "/maintenance", map[string]bool{"frozen": frozen}, nil)
}
