package openapi

import (
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
