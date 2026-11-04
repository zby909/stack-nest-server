package deployclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAppRetryKeepsRequestAndGeneration 模拟提交成功后响应丢失，重试必须保持同一请求和基线。
func TestAppRetryKeepsRequestAndGeneration(t *testing.T) {
	posts, gets := 0, 0
	var original map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("authorization missing")
		}
		if r.Method == "GET" {
			gets++
			if r.URL.Path != "/api/app-deploy/v1/app" {
				t.Error("wrong endpoint", r.URL.Path)
			}
			fmt.Fprint(w, `{"appId":"app-fixture","slug":"demo","generation":7,"status":"ready"}`)
			return
		}
		posts++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if posts == 1 {
			original = body
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"temporary"}`)
			return
		}
		if fmt.Sprint(body) != fmt.Sprint(original) || body["expectedGeneration"] != float64(7) || body["requestId"] == "" {
			t.Error("retried a different deployment", body, original)
		}
		w.WriteHeader(202)
		fmt.Fprint(w, `{"appId":"app-fixture","slug":"demo","generation":8,"status":"pending"}`)
	}))
	defer server.Close()
	c, _ := New(server.URL, "fixture-token")
	c.Wait = func(context.Context, time.Duration) error { return nil }
	result, err := c.DeployApplication(context.Background(), "ghcr.io/owner/app:v2", 0, "")
	if err != nil || result.Generation != 8 || gets != 1 || posts != 2 {
		t.Fatal("retry", result, gets, posts, err)
	}
}

// TestAppWaitRejectsFailureAndOtherGeneration 不把旧容器仍运行或后来的任务当作本次成功。
func TestAppWaitRejectsFailureAndOtherGeneration(t *testing.T) {
	for _, code := range []int{200, 409, 401} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("generation") != "8" {
				t.Error("missing generation binding")
			}
			w.WriteHeader(code)
			if code == 200 {
				fmt.Fprint(w, `{"appId":"fixture","slug":"demo","generation":8,"status":"error","error":"health failed"}`)
			} else {
				fmt.Fprint(w, `{"error":"task changed"}`)
			}
		}))
		c, _ := New(server.URL, "fixture-token")
		var progress bytes.Buffer
		if _, err := c.WaitApplication(context.Background(), "fixture", 8, &progress); err == nil {
			t.Error("failure reported as success", code)
		}
		server.Close()
	}
}

// TestAppNoWaitIsOnlyAReceipt 返回排队回执并明确不等同于健康上线，取消等待不取消后台任务。
func TestAppNoWaitIsOnlyAReceipt(t *testing.T) {
	gets, posts := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gets++
			fmt.Fprint(w, `{"appId":"fixture","slug":"demo","generation":1,"status":"ready"}`)
			return
		}
		if r.Method != "POST" {
			t.Error("unexpected lifecycle operation", r.Method)
		}
		posts++
		w.WriteHeader(202)
		fmt.Fprint(w, `{"appId":"fixture","slug":"demo","generation":2,"status":"pending"}`)
	}))
	defer server.Close()
	t.Setenv("STACKNEST_URL", server.URL)
	t.Setenv("STACKNEST_DEPLOY_TOKEN", "fixture-token")
	var out, progress bytes.Buffer
	if code := Main(context.Background(), []string{"app", "deploy", "--image", "example/app:v2", "--no-wait"}, &out, &progress); code != 0 || gets != 1 || posts != 1 || !strings.Contains(out.String(), `"status": "pending"`) {
		t.Fatal("no-wait", code, out.String(), progress.String(), gets, posts)
	}
	// 初始状态专用响应，验证取消只终止客户端等待。
	waitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("wait canceled a task")
		}
		fmt.Fprint(w, `{"appId":"fixture","slug":"demo","generation":2,"status":"pending"}`)
	}))
	defer waitServer.Close()
	c, _ := New(waitServer.URL, "fixture-token")
	c.Wait = func(context.Context, time.Duration) error { return context.Canceled }
	if _, err := c.WaitApplication(context.Background(), "fixture", 2, &progress); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel wait", err)
	}
}
