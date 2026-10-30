package containerapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type failureLogRuntime struct {
	testRuntime
	text string
}

// DeploymentLogs 模拟已保存候选日志中仍需按当前配置遮盖的值。
func (r *failureLogRuntime) DeploymentLogs(context.Context, string, int64) (string, error) {
	return r.text, nil
}

// TestDeploymentLogServiceRedactionAndLegacy 新配置预先脱敏后，旧配置仍被再次保护；没有旧快照时明确说明。
func TestDeploymentLogServiceRedactionAndLegacy(t *testing.T) {
	id, secret := "0c2d84bb-15a1-492b-938a-83282600c112", "test-only-deployment-log-secret"
	spec := testSpec()
	spec.Env = map[string]string{"SECRET": "old-private-value"}
	sealed, _ := seal(secret, id, spec)
	s := New(nil, &failureLogRuntime{text: "candidate=[已隐藏] old=old-private-value"}, secret, nil, nil)
	text, err := s.DeploymentLogs(context.Background(), App{ID: id, Generation: 3, Sealed: sealed})
	if err != nil || strings.Contains(text, "old-private-value") || !strings.Contains(text, "candidate=[已隐藏]") {
		t.Fatal(text, err)
	}
	s.runtime = &testRuntime{}
	text, err = s.DeploymentLogs(context.Background(), App{ID: id, Generation: 3, Error: "previous failure"})
	if err != nil || !strings.Contains(text, "previous failure") || !strings.Contains(text, "本次没有保存") {
		t.Fatal(text, err)
	}
}

// TestEscapedLogJSONResponse UTF-8 和 HTML 转义膨胀不会让合法的 256 KiB 日志在私有响应中被截断。
func TestEscapedLogJSONResponse(t *testing.T) {
	text := strings.Repeat("<&>", 80000)
	body, _ := json.Marshal(map[string]string{"text": text})
	client := &Client{client: &http.Client{Transport: timeoutTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}}
	got, err := client.DeploymentLogs(context.Background(), "0c2d84bb-15a1-492b-938a-83282600c112", 3)
	if err != nil || got != text {
		t.Fatal("valid escaped logs truncated", err, len(got))
	}
}
