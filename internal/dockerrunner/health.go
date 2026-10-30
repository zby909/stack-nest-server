package dockerrunner

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"stacknest/server/internal/containerapp"
)

// healthClient 禁止环境代理和跨地址重定向，每次探测最多两秒。
var healthClient = &http.Client{
	Timeout:       2 * time.Second,
	Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

// probe 仅对已确认容器所在私有网络的地址执行检查。
func (r *Runner) probe(ctx context.Context, value inspection, id string) error {
	address, err := r.address(value, id)
	if err != nil {
		return err
	}
	return probeAddress(ctx, address, value.Config.Labels[healthPathLabel])
}

// probeAddress 检查端口或 2xx HTTP 响应，不读取、记录应用响应内容。
func probeAddress(ctx context.Context, address, healthPath string) error {
	if err := containerapp.ValidateHealthPath(healthPath); err != nil {
		return err
	}
	if healthPath == "" {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
		if err == nil {
			conn.Close()
		}
		if err != nil {
			return errors.New("TCP 健康检查连接失败")
		}
		return nil
	}
	target := (&url.URL{Scheme: "http", Host: address, Path: healthPath}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	response, err := healthClient.Do(req)
	if err != nil {
		return errors.New("HTTP 健康检查连接失败")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP 健康检查返回 %d", response.StatusCode)
	}
	return nil
}
