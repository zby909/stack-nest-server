package dockerrunner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// startupRunner 用内存 Docker 响应和虚拟时钟检查等待时限，不创建真实容器。
func startupRunner(state func(*inspection)) *Runner {
	const id = "0c2d84bb-15a1-492b-938a-83282600c112"
	runner := &Runner{instance: "startup-test"}
	runner.engine = &Engine{client: &http.Client{Transport: pullTransport(func(req *http.Request) (*http.Response, error) {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		value := inspection{ID: "aaaaaaaaaaaa"}
		value.Config.Labels = map[string]string{instanceLabel: runner.instance, appLabel: id}
		value.HostConfig.NetworkMode = runner.networkName(id)
		value.State.Status = "created"
		state(&value)
		body, _ := json.Marshal(value)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}}
	return runner
}

// TestStartupWaitBudget 验证长等待不再被 60 秒截断，所有配置仍有截止时间。
func TestStartupWaitBudget(t *testing.T) {
	for _, seconds := range []int{30, 60, 180, 300} {
		t.Run((time.Duration(seconds) * time.Second).String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				runner := startupRunner(func(*inspection) {})
				err := runner.ready(context.Background(), "0c2d84bb-15a1-492b-938a-83282600c112", "aaaaaaaaaaaa", time.Duration(seconds)*time.Second)
				if err == nil || time.Since(start) != time.Duration(seconds)*time.Second {
					t.Fatal("incorrect wait budget", time.Since(start), err)
				}
			})
		})
	}
}

// TestStartupFailureAndCancellation 确认明确退出、OOM、持续重启及取消无需等满配置时间。
func TestStartupFailureAndCancellation(t *testing.T) {
	for _, scenario := range []string{"oom", "exited", "restarts", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				runner := startupRunner(func(value *inspection) {
					if time.Since(start) < 90*time.Second {
						return
					}
					switch scenario {
					case "oom":
						value.State.OOMKilled = true
					case "exited":
						value.State.Status, value.State.ExitCode = "exited", 1
					case "restarts":
						value.RestartCount = 3
					case "cancel":
						cancel()
					}
				})
				err := runner.ready(ctx, "0c2d84bb-15a1-492b-938a-83282600c112", "aaaaaaaaaaaa", 300*time.Second)
				if err == nil || time.Since(start) != 90*time.Second {
					t.Fatal("failure was not reported immediately", time.Since(start), err)
				}
				if scenario == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal("lost cancellation", err)
				}
			})
		})
	}
}
