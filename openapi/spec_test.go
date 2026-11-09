package openapi

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestDocumentReferences 确保公开契约已合并、没有外部文件依赖，所有本地引用存在。
func TestDocumentReferences(t *testing.T) {
	var root map[string]any
	if err := yaml.Unmarshal(Document, &root); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if ref, ok := value["$ref"].(string); ok {
				if !strings.HasPrefix(ref, "#/") {
					t.Fatal("unexpected external reference", ref)
				}
				var target any = root
				for _, key := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					object, ok := target.(map[string]any)
					if !ok || object[key] == nil {
						t.Fatal("missing reference", ref)
					}
					target = object[key]
				}
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(root)
}

// TestDocumentIncludesSupplements 防止新契约遗漏、同名版本模型混用或 Bearer 鉴权变成 Cookie。
func TestDocumentIncludesSupplements(t *testing.T) {
	var root map[string]any
	if err := yaml.Unmarshal(Document, &root); err != nil {
		t.Fatal(err)
	}
	paths := root["paths"].(map[string]any)
	entries, _ := specifications.ReadDir(".")
	for _, entry := range entries {
		body, _ := specifications.ReadFile(entry.Name())
		var source map[string]any
		if err := yaml.Unmarshal(body, &source); err != nil {
			t.Fatal(err)
		}
		routes, _ := source["paths"].(map[string]any)
		for path := range routes {
			if paths[path] == nil {
				t.Errorf("missing %s from %s", path, entry.Name())
			}
		}
	}
	for path, scheme := range map[string]string{
		"/api/deploy/v1/project":       "DeployToken",
		"/api/app-deploy/v1/app":       "AppDeployToken",
		"/api/deploy-tools/skill":      "SecureSessionCookie",
		"/api/apps/{id}/deploy-tokens": "SecureSessionCookie",
	} {
		op := paths[path].(map[string]any)["get"].(map[string]any)
		security := op["security"].([]any)
		if security[0].(map[string]any)[scheme] == nil {
			t.Errorf("incorrect authentication for %s: %v", path, security)
		}
	}
	schemas := root["components"].(map[string]any)["schemas"].(map[string]any)
	static := schemas["Deployment"].(map[string]any)["properties"].(map[string]any)
	app := schemas["AppMaintenanceDeployment"].(map[string]any)["properties"].(map[string]any)
	if static["generation"] != nil || app["generation"] == nil {
		t.Fatal("static and app deployment schemas collided")
	}
	token := schemas["AutomationToken"].(map[string]any)["properties"].(map[string]any)
	lastUsed := token["lastUsedAt"].(map[string]any)
	if !reflect.DeepEqual(lastUsed["type"], []any{"string", "null"}) || lastUsed["nullable"] != nil {
		t.Fatal("OpenAPI 3.0 nullable was not converted to 3.1")
	}
}

// TestBundleRejectsConflicts 确保重复路由和改变语义的鉴权声明不会静默覆盖。
func TestBundleRejectsConflicts(t *testing.T) {
	for _, conflicting := range []string{
		"paths: {/example: {get: {responses: {}}}}",
		"components: {securitySchemes: {SessionCookie: {type: http, scheme: bearer}}}",
	} {
		documents := map[string]map[string]any{}
		for file, text := range map[string]string{
			"openapi.yaml":    "openapi: 3.1.0\npaths: {/example: {get: {responses: {}}}}\ncomponents: {securitySchemes: {SessionCookie: {type: apiKey, in: cookie, name: session}}}",
			"components.yaml": "components: {}",
			"additional.yaml": conflicting,
		} {
			var doc map[string]any
			if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
				t.Fatal(err)
			}
			documents[file] = doc
		}
		if _, err := bundle(documents); err == nil {
			t.Fatal("conflicting document accepted")
		}
	}
}
