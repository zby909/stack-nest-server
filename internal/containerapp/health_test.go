package containerapp

import (
	"strings"
	"testing"
)

// TestHealthPathRejectsAddressInjection 验证可选检查路径不能携带外部地址或重写目标。
func TestHealthPathRejectsAddressInjection(t *testing.T) {
	for _, path := range []string{"", "/", "/health", "/api/healthz", "/api/healthz/", "/release.v2/health"} {
		spec := testSpec()
		spec.HealthPath = path
		if err := spec.Validate(); err != nil {
			t.Fatal(path, err)
		}
	}
	for _, path := range []string{"https://example.com/health", "//example.com/health", "health", "/health?target=127.0.0.1", "/health#ok", "/a/../health", "/health%3fsecret", "/health\r\nHost: localhost", "/health\\path", "/" + strings.Repeat("a", 256)} {
		spec := testSpec()
		spec.HealthPath = path
		if spec.Validate() == nil {
			t.Fatalf("unsafe health path accepted: %q", path)
		}
	}
}
