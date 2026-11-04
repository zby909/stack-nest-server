package deployclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const appAPIPrefix = "/api/app-deploy/v1/"

// Application 是自动部署任务的安全摘要，不含运行环境和仓库凭据。
type Application struct {
	AppID      string `json:"appId"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Image      string `json:"image"`
	Generation int64  `json:"generation"`
	Status     string `json:"status"`
	Stage      string `json:"stage,omitempty"`
	URL        string `json:"url"`
	Error      string `json:"error,omitempty"`
	Canceled   bool   `json:"canceled,omitempty"`
}

// Application 查询唯一绑定的应用，可要求代次一致，防止跟随另一个部署任务。
func (c *Client) Application(ctx context.Context, generation int64) (Application, error) {
	path := appAPIPrefix + "app"
	if generation > 0 {
		path += "?generation=" + url.QueryEscape(strconv.FormatInt(generation, 10))
	}
	var value Application
	err := c.retry(ctx, func() error { return c.requestEndpoint(ctx, "GET", path, nil, nil, &value) })
	if err == nil && (value.AppID == "" || value.Slug == "" || value.Generation < 1 || generation > 0 && value.Generation != generation) {
		err = errors.New("应用响应不完整或部署任务已变化")
	}
	return value, err
}

// DeployApplication 有界重试使用同一请求编号及基线，不重新查询后盲目覆盖新版本。
func (c *Client) DeployApplication(ctx context.Context, image string, expected int64, requestID string) (Application, error) {
	current, err := c.Application(ctx, 0)
	if err != nil {
		return Application{}, err
	}
	if expected == 0 {
		expected = current.Generation
	}
	if requestID == "" {
		requestID = uuid.NewString()
	}
	body, _ := json.Marshal(map[string]any{"image": image, "expectedGeneration": expected, "requestId": requestID})
	var result Application
	err = c.retry(ctx, func() error { return c.requestEndpoint(ctx, "POST", appAPIPrefix+"deploy", body, nil, &result) })
	if err != nil {
		return result, fmt.Errorf("提交未确认；请先核对应用状态。request-id=%s expected-generation=%d：%w", requestID, expected, err)
	}
	if result.AppID != current.AppID || result.Generation != expected+1 {
		return Application{}, errors.New("应用或任务代次与提交不符，请在后台核实")
	}
	return result, nil
}

// WaitApplication 只等待指定代次；超时或断线不会取消后台任务，也不会误报后续版本成功。
func (c *Client) WaitApplication(ctx context.Context, appID string, generation int64, progress io.Writer) (Application, error) {
	last, reported := "", time.Time{}
	for {
		value, err := c.Application(ctx, generation)
		if err != nil {
			return value, err
		}
		if value.AppID != appID {
			return value, errors.New("应用身份发生变化，已停止等待")
		}
		switch value.Status {
		case "ready":
			return value, nil
		case "error", "stopped":
			return value, fmt.Errorf("部署未成功（代次 %d）：%s；请在后台查看失败详情", generation, value.Error)
		case "pending", "deploying":
		default:
			return value, errors.New("应用任务状态未知，已停止等待")
		}
		state := value.Status + " " + value.Stage
		if state != last || time.Since(reported) >= time.Minute {
			fmt.Fprintf(progress, "应用 %s · 代次 %d · %s\n", value.Slug, generation, state)
			last, reported = state, time.Now()
		}
		if err = c.Wait(ctx, 4*time.Second); err != nil {
			return value, err
		}
	}
}
