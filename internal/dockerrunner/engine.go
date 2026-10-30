package dockerrunner

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
	"time"
)

// Engine 仅通过主机 Unix socket 使用已验证的 Docker API。
type Engine struct {
	client  *http.Client
	version string
}

// NewEngine 创建不访问任意 TCP 地址的 Docker 客户端。
func NewEngine(socket string) *Engine {
	return &Engine{client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, MaxIdleConns: 16, IdleConnTimeout: 30 * time.Second}}}
}

// request 不回显 Docker 错误正文，避免环境变量与私有仓库凭据出现在响应里。
func (e *Engine) request(ctx context.Context, method, path string, body any, headers map[string]string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	version := e.version
	if version == "" {
		version = "1.45"
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker/v"+version+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := e.client.Do(req)
	if err != nil {
		return nil, errors.New("Docker 连接失败")
	}
	if response.StatusCode >= 400 {
		response.Body.Close()
		if response.StatusCode == 404 {
			return nil, errMissing
		}
		return nil, fmt.Errorf("Docker 操作失败（%d）", response.StatusCode)
	}
	return response, nil
}

// call 读取有界 JSON 或关闭无内容响应。
func (e *Engine) call(ctx context.Context, method, path string, body, target any) error {
	response, err := e.request(ctx, method, path, body, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if target != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(target)
	}
	return nil
}

var errMissing = errors.New("Docker resource missing")

// inspection 只读取运行器需要的字段，完整 inspect 不跨越私有 socket。
type inspection struct {
	ID           string `json:"Id"`
	RestartCount int
	GraphDriver  struct {
		Name string
		Data map[string]string
	}
	Image  string
	Config struct {
		Labels  map[string]string
		Volumes map[string]any
	}
	HostConfig struct {
		Memory      int64
		NanoCPUs    int64
		Privileged  bool
		SecurityOpt []string
		NetworkMode string
	}
	State struct {
		Running   bool
		Status    string
		OOMKilled bool
		ExitCode  int
		Error     string
	}
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
	Mounts []struct {
		Type, Source, Destination, Name string
		RW                              bool
	}
}

// inspect 获取指定资源，调用方仍须验证托管标签。
func (e *Engine) inspect(ctx context.Context, id string) (inspection, error) {
	var value inspection
	err := e.call(ctx, "GET", "/containers/"+url.PathEscape(id)+"/json", nil, &value)
	return value, err
}

// containerSummary 只保留编号、标签与资源使用所需状态。
type containerSummary struct {
	ID     string `json:"Id"`
	Labels map[string]string
	State  string
}

// containers 对 daemon 本身按托管实例标签筛选，再逐个检查。
func (r *Runner) containers(ctx context.Context, id string) ([]containerSummary, error) {
	labels := []string{instanceLabel + "=" + r.instance}
	if id != "" {
		labels = append(labels, appLabel+"="+id)
	}
	filters, _ := json.Marshal(map[string][]string{"label": labels})
	var values []containerSummary
	err := r.engine.call(ctx, "GET", "/containers/json?all=1&filters="+url.QueryEscape(string(filters)), nil, &values)
	return values, err
}

// stateResponse 转换安全的运行状态与退出原因。
func stateResponse(value inspection) result {
	return result{ContainerID: value.ID, ImageID: value.Image, State: value.State.Status, OOMKilled: value.State.OOMKilled, ExitCode: value.State.ExitCode}
}

// stop 给程序正常退出时间，不接受外部传入任意容器编号。
func (r *Runner) stop(ctx context.Context, id string) error {
	return r.engine.call(ctx, "POST", "/containers/"+url.PathEscape(id)+"/stop?t=15", nil, nil)
}

// start 启动已确认归属于本应用的容器。
func (r *Runner) start(ctx context.Context, id string) error {
	if err := r.checkEgress(); err != nil {
		return err
	}
	return r.engine.call(ctx, "POST", "/containers/"+url.PathEscape(id)+"/start", nil, nil)
}

// remove 清理本应用容器及其匿名卷。
func (r *Runner) remove(ctx context.Context, id string) error {
	return r.engine.call(ctx, "DELETE", "/containers/"+url.PathEscape(id)+"?force=1&v=1", nil, nil)
}

// candidateName 为每一持久化代次生成确定名称，方便中断后重试。
func (r *Runner) candidateName(id string, generation int64) string {
	return "stacknest-app-" + r.instance + "-" + id + "-" + strconv.FormatInt(generation, 10)
}
