package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"stacknest/server/internal/containerapp"
)

// TestAppEscapedConfiguration 验证合法环境变量和仓库凭据经 JSON 转义后仍能被读取。
func TestAppEscapedConfiguration(t *testing.T) {
	input := containerapp.Input{Name: "Example", Slug: "example", Spec: containerapp.Spec{
		Image: "example/app:v1", Port: 3000, CPUs: 0.5, MemoryMiB: 128,
		Env: map[string]string{"A": strings.Repeat("<", 4095), "B": strings.Repeat("&", 4095),
			"C": strings.Repeat(">", 4095), "D": strings.Repeat("<", 4095)},
		Registry: &containerapp.Registry{Username: "user", Password: strings.Repeat("&", 4096)},
	}}
	if err := input.Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 32768 {
		t.Fatal("fixture did not exercise JSON expansion")
	}
	req := httptest.NewRequest("POST", "/api/apps", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	var decoded containerapp.Input
	if !readAppJSON(out, req, &decoded) || decoded.Spec.Validate() != nil ||
		decoded.Env["A"] != input.Env["A"] || decoded.Registry.Password != input.Registry.Password {
		t.Fatalf("legal configuration rejected: status=%d bytes=%d", out.Code, len(body))
	}
}

// TestAppJSONLimit 拒绝超过整体上限和多份 JSON，字段大小由 Spec 单独限制。
func TestAppJSONLimit(t *testing.T) {
	for _, body := range []string{strings.Repeat(" ", containerapp.ConfigJSONLimit) + "{}", "{} {}"} {
		out := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/apps", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		var decoded containerapp.Input
		if readAppJSON(out, req, &decoded) || out.Code != 400 {
			t.Fatalf("invalid body accepted: %d", out.Code)
		}
	}
	spec := containerapp.Spec{Image: "example/app:v1", Port: 3000, CPUs: 0.5, MemoryMiB: 128,
		DataPath: "/" + strings.Repeat("a", 4096)}
	if spec.Validate() == nil {
		t.Fatal("oversized persistent path accepted")
	}
}
