package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCounterPersistence 验证验收镜像确实使用文件保存状态，重建服务后仍保留。
func TestCounterPersistence(t *testing.T) {
	dir := t.TempDir()
	first := handler(dir, "v1")
	req := httptest.NewRequest("POST", "/api/counter", nil)
	out := httptest.NewRecorder()
	first.ServeHTTP(out, req)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"count":1`) {
		t.Fatal(out.Code, out.Body.String())
	}
	next := handler(dir, "v2")
	out = httptest.NewRecorder()
	next.ServeHTTP(out, httptest.NewRequest("GET", "/api/counter", nil))
	if !strings.Contains(out.Body.String(), `"count":1`) || !strings.Contains(out.Body.String(), `"version":"v2"`) {
		t.Fatal(out.Body.String())
	}
}
