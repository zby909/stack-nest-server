package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Client 通过私有 Unix socket 调用固定接口，不接受任意宿主命令。
type Client struct{ http *http.Client }

// NewClient 创建有时限的本地网络配置客户端。
func NewClient(socket string) *Client {
	transport := &http.Transport{Proxy: nil, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if socket == "" {
			return nil, ErrUnavailable
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &Client{http: &http.Client{Transport: transport, Timeout: 25 * time.Second}}
}

// call 只使用固定路径和结构化 JSON，区分版本冲突及配置错误。
func (c *Client) call(ctx context.Context, method string, input any) (View, error) {
	var view View
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return view, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://egress/config", body)
	if err != nil {
		return view, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return view, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		if response.StatusCode == 409 {
			return view, ErrConflict
		}
		if response.StatusCode != 400 {
			return view, ErrUnavailable
		}
		var failure struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure) != nil || failure.Error == "" {
			return view, ErrUnavailable
		}
		return view, fmt.Errorf("网络配置无效：%s", failure.Error)
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&view)
	if err != nil {
		return view, ErrUnavailable
	}
	return view, nil
}

// Get 读取宿主当前配置及实际保护范围。
func (c *Client) Get(ctx context.Context) (View, error) { return c.call(ctx, "GET", nil) }

// Update 保存经过宿主重新校验的配置，并核对乐观锁版本。
func (c *Client) Update(ctx context.Context, input Update) (View, error) {
	return c.call(ctx, "PUT", input)
}
