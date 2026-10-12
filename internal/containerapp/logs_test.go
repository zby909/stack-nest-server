package containerapp

import (
	"context"
	"strings"
	"testing"
)

type logRuntime struct {
	testRuntime
	text string
}

// Logs 返回带有重叠敏感值的日志，验证完整凭据不会被短值提前拆开。
func (r *logRuntime) Logs(context.Context, string, string) (string, error) { return r.text, nil }

// TestOverlappingLogSecrets 验证长短凭据、仓库密码和替换标记均只处理一次。
func TestOverlappingLogSecrets(t *testing.T) {
	secret, id := "thirty-two-byte-test-secret-only", "0c2d84bb-15a1-492b-938a-83282600c112"
	spec := testSpec()
	spec.Env = map[string]string{"SHORT": "hidden", "LONG": "hidden-test-value", "MARKER": "已隐藏"}
	spec.Registry = &Registry{Username: "fixture", Password: "hidden-registry-password"}
	sealed, err := seal(secret, id, spec)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &logRuntime{text: "env=hidden-test-value registry=hidden-registry-password short=hidden marker=已隐藏"}
	service := New(nil, runtime, secret, nil, nil)
	for i := 0; i < 100; i++ {
		text, err := service.Logs(context.Background(), App{ID: id, Sealed: sealed})
		want := "env=[已隐藏] registry=[已隐藏] short=[已隐藏] marker=[已隐藏]"
		if err != nil || text != want || strings.Contains(text, "-password") {
			t.Fatalf("redaction leaked or changed marker: %q err=%v", text, err)
		}
	}
}

// TestUpdateLogsRedactCurrentAndActiveConfigs 更新排队时旧容器仍可输出旧值，两套环境和仓库密码都不能回显。
func TestUpdateLogsRedactCurrentAndActiveConfigs(t *testing.T) {
	secret, id := "thirty-two-byte-test-secret-only", "0c2d84bb-15a1-492b-938a-83282600c112"
	oldSpec, newSpec := testSpec(), testSpec()
	oldSpec.Env = map[string]string{"PASSWORD": "old-config-secret"}
	newSpec.Env = map[string]string{"PASSWORD": "new-config-secret"}
	oldSpec.Registry = &Registry{Username: "fixture", Password: "old-registry-secret"}
	newSpec.Registry = &Registry{Username: "fixture", Password: "new-registry-secret"}
	active, err := seal(secret, id, oldSpec)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := seal(secret, id, newSpec)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &logRuntime{text: "old=old-config-secret next=new-config-secret registry=old-registry-secret,next=new-registry-secret"}
	service := New(nil, runtime, secret, nil, nil)
	for _, status := range []string{"pending", "deploying"} {
		text, err := service.Logs(context.Background(), App{ID: id, Status: status, Sealed: sealed, ActiveSealed: active})
		if err != nil {
			t.Fatal(err)
		}
		if text != "old=[已隐藏] next=[已隐藏] registry=[已隐藏],next=[已隐藏]" {
			t.Errorf("unredacted update logs in %s: %q", status, text)
		}
	}
}

// TestLogsRejectUnreadableActiveConfig 无法解密旧容器配置时不返回可能带旧凭据的正文。
func TestLogsRejectUnreadableActiveConfig(t *testing.T) {
	secret, id := "thirty-two-byte-test-secret-only", "0c2d84bb-15a1-492b-938a-83282600c112"
	sealed, err := seal(secret, id, testSpec())
	if err != nil {
		t.Fatal(err)
	}
	service := New(nil, &logRuntime{text: "older secret log"}, secret, nil, nil)
	text, err := service.Logs(context.Background(), App{ID: id, Sealed: sealed, ActiveSealed: "invalid-ciphertext"})
	if err == nil || text != "" {
		t.Fatal("log body exposed without active config", text, err)
	}
}
