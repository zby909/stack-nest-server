// Package deployclient 供本地终端和 CI 调用同一套自动部署协议。
package deployclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const apiPrefix = "/api/deploy/v1/"

// Client 固定单一管理端来源，不跟随可能泄漏 Token 的重定向。
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	Wait    func(context.Context, time.Duration) error
}
type Version struct {
	ID         string `json:"id"`
	PreviewURL string `json:"previewUrl"`
}
type Project struct {
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	SiteURL     string    `json:"siteUrl"`
	Draft       *Version  `json:"draft"`
	Deployments []Version `json:"deployments"`
}
type Status struct {
	ID           string `json:"id"`
	Offset       int64  `json:"offset"`
	Size         int64  `json:"size"`
	ChunkSize    int64  `json:"chunkSize"`
	MinChunkSize int64  `json:"minChunkSize"`
	Completed    bool   `json:"completed"`
}
type Result struct {
	ProjectID  string `json:"projectId"`
	Slug       string `json:"slug"`
	DraftID    string `json:"draftId"`
	ExpectedID string `json:"expectedId"`
	PreviewURL string `json:"previewUrl"`
	SiteURL    string `json:"siteUrl"`
	Status     string `json:"status"`
}

// APIError 只保留有界错误和追踪编号，不输出 HTML 响应或请求授权头。
type APIError struct {
	Status     int
	Message    string
	RequestID  string
	RetryAfter time.Duration
}

// Error 返回适合终端和 CI 日志的安全提示。
func (e *APIError) Error() string {
	return fmt.Sprintf("HTTP %d: %s (request: %s)", e.Status, e.Message, e.RequestID)
}

// New 除本机 SSH 隧道外只接受 HTTPS，拒绝 URL 中的密码、查询参数和路径。
func New(base, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("STACKNEST_URL 必须是管理端完整地址，例如 https://stack-nest.example.com")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, errors.New("必须使用 HTTPS；HTTP 仅用于 localhost / 回环地址的本机隧道")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n\t ") {
		return nil, errors.New("请通过 STACKNEST_DEPLOY_TOKEN 或 --token-file 提供凭据")
	}
	u.Path = ""
	return &Client{BaseURL: strings.TrimRight(u.String(), "/"), Token: token, HTTP: &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Wait: wait}, nil
}

// wait 支持 Ctrl+C 中止重试等待。
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// request 单次发送固定来源请求；响应大小与错误文本均有上限。
func (c *Client) request(ctx context.Context, method, path string, body []byte, offset *int64, out any) error {
	r, err := http.NewRequestWithContext(ctx, method, c.BaseURL+apiPrefix+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+c.Token)
	r.Header.Set("Accept", "application/json")
	if offset != nil {
		r.Header.Set("Content-Type", "application/octet-stream")
		r.Header.Set("Upload-Offset", strconv.FormatInt(*offset, 10))
	} else if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := c.HTTP.Do(r)
	if err != nil {
		return fmt.Errorf("连接管理端失败：%w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return errors.New("管理端响应过大，已停止操作")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := "管理端请求失败，请检查服务状态"
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" && len(failure.Error) < 2048 {
			message = failure.Error
		}
		message = strings.ReplaceAll(message, c.Token, "[redacted]")
		seconds, _ := strconv.Atoi(response.Header.Get("Retry-After"))
		seconds = max(1, min(10, seconds))
		return &APIError{Status: response.StatusCode, Message: message, RequestID: response.Header.Get("X-Request-ID"), RetryAfter: time.Duration(seconds) * time.Second}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("管理端未返回有效 JSON，请确认地址指向 StackNest 管理后台")
	}
	return nil
}

// retryable 只重试可恢复的网络错误，不循环重试权限、校验和容量错误。
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var api *APIError
	if errors.As(err, &api) {
		return api.Status == 408 || api.Status == 429 || api.Status == 500 || api.Status == 502 || api.Status == 503 || api.Status == 504
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// retry 有界重试幂等操作，连续失败最多发送四次请求。
func (c *Client) retry(ctx context.Context, call func() error) error {
	for attempt := 0; ; attempt++ {
		err := call()
		if err == nil || attempt == 3 || !retryable(err) {
			return err
		}
		delay := time.Duration(1<<attempt) * time.Second
		var api *APIError
		if errors.As(err, &api) {
			delay = max(delay, api.RetryAfter)
		}
		if err := c.Wait(ctx, delay); err != nil {
			return err
		}
	}
}

// Project 查询 Token 绑定的唯一项目。
func (c *Client) Project(ctx context.Context) (Project, error) {
	var p Project
	err := c.retry(ctx, func() error { return c.request(ctx, "GET", "project", nil, nil, &p) })
	if err == nil && (p.ID == "" || p.Slug == "") {
		err = errors.New("项目响应不完整")
	}
	return p, err
}

// currentID 返回正式版本，用于并发发布时的比较并交换。
func currentID(p Project) string {
	if len(p.Deployments) > 0 {
		return p.Deployments[0].ID
	}
	return ""
}

// Publish 仅发布用户明确指定的草稿与基线；不读取并盲目发布“最新”版本。
func (c *Client) Publish(ctx context.Context, draftID, expectedID string) (Result, error) {
	if !strings.HasPrefix(draftID, "dep-") || len(draftID) > 100 || len(expectedID) > 100 {
		return Result{}, errors.New("请提供确切的 --draft-id 和 --expected-id（首次发布填 none）")
	}
	body, _ := json.Marshal(map[string]string{"draftId": draftID, "expectedId": expectedID})
	var p Project
	err := c.retry(ctx, func() error { return c.request(ctx, "POST", "publish", body, nil, &p) })
	if err != nil {
		return Result{}, err
	}
	if currentID(p) != draftID {
		return Result{}, errors.New("正式版本与指定版本不符，请到后台核实")
	}
	return Result{ProjectID: p.ID, Slug: p.Slug, DraftID: draftID, ExpectedID: expectedID, SiteURL: p.SiteURL, Status: "published"}, nil
}
