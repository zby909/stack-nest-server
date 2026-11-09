package openapi

import (
	"embed"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed *.yaml
var specifications embed.FS

// Document 将主契约和补充接口合并，保留各自鉴权要求且不依赖外部引用文件。
var Document = document()

// document 自动发现同目录 OpenAPI 文件，生成配置不参与合并。
func document() []byte {
	documents := map[string]map[string]any{}
	entries, err := specifications.ReadDir(".")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		body, err := specifications.ReadFile(entry.Name())
		if err != nil {
			panic(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(body, &doc); err != nil {
			panic(err)
		}
		if doc["openapi"] != nil {
			documents[entry.Name()] = doc
		}
	}
	body, err := bundle(documents)
	if err != nil {
		panic(err)
	}
	return body
}

// componentName 给补充模型加文件前缀，避免静态版本与应用部署等同名模型互相覆盖。
func componentName(file, category, name string) string {
	if file == "openapi.yaml" || file == "components.yaml" || category == "securitySchemes" {
		return name
	}
	var prefix string
	for _, part := range strings.Split(strings.TrimSuffix(file, ".yaml"), "-") {
		prefix += strings.ToUpper(part[:1]) + part[1:]
	}
	return prefix + name
}

// rewriteReferences 转换组件引用和旧版 nullable，同时拒绝未收录的外部契约。
func rewriteReferences(value any, file string, documents map[string]map[string]any) error {
	switch value := value.(type) {
	case map[string]any:
		if ref, ok := value["$ref"].(string); ok {
			target, pointer, found := strings.Cut(ref, "#/")
			if target == "" {
				target = file
			}
			target = strings.TrimPrefix(target, "./")
			parts := strings.Split(pointer, "/")
			if !found || documents[target] == nil || len(parts) < 3 || parts[0] != "components" {
				return fmt.Errorf("%s: unsupported reference %s", file, ref)
			}
			parts[2] = componentName(target, parts[1], parts[2])
			value["$ref"] = "#/" + strings.Join(parts, "/")
		}
		if nullable, exists := value["nullable"]; exists {
			if nullable == true {
				typeName, ok := value["type"].(string)
				if !ok {
					return fmt.Errorf("%s: nullable schema needs an explicit type", file)
				}
				value["type"] = []any{typeName, "null"}
			}
			delete(value, "nullable")
		}
		for _, child := range value {
			if err := rewriteReferences(child, file, documents); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := rewriteReferences(child, file, documents); err != nil {
				return err
			}
		}
	}
	return nil
}

// sameComponent 允许重复声明相同鉴权方式，说明文字差异不影响鉴权语义。
func sameComponent(a, b any) bool {
	strip := func(value any) any {
		if object, ok := value.(map[string]any); ok {
			copy := map[string]any{}
			for key, child := range object {
				if key != "description" {
					copy[key] = child
				}
			}
			return copy
		}
		return value
	}
	return reflect.DeepEqual(strip(a), strip(b))
}

// bundle 合并路由、组件和默认鉴权；重复路由或定义冲突会阻止生成。
func bundle(documents map[string]map[string]any) ([]byte, error) {
	root := documents["openapi.yaml"]
	if root == nil || documents["components.yaml"] == nil {
		return nil, fmt.Errorf("missing main OpenAPI documents")
	}
	paths, components := map[string]any{}, map[string]any{}
	files := []string{}
	for name := range documents {
		if name != "openapi.yaml" && name != "components.yaml" {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	// 主契约优先保留完整说明，补充文件按名称稳定合并。
	files = append([]string{"openapi.yaml", "components.yaml"}, files...)
	for _, file := range files {
		doc := documents[file]
		if err := rewriteReferences(doc, file, documents); err != nil {
			return nil, err
		}
		sections, _ := doc["components"].(map[string]any)
		for category, section := range sections {
			definitions, ok := section.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s: invalid components", file)
			}
			if components[category] == nil {
				components[category] = map[string]any{}
			}
			destination := components[category].(map[string]any)
			for name, definition := range definitions {
				key := componentName(file, category, name)
				if existing, exists := destination[key]; exists {
					if !sameComponent(existing, definition) {
						return nil, fmt.Errorf("%s: conflicting component %s/%s", file, category, key)
					}
				} else {
					destination[key] = definition
				}
			}
		}
		routes, _ := doc["paths"].(map[string]any)
		for path, item := range routes {
			if _, exists := paths[path]; exists {
				return nil, fmt.Errorf("%s: duplicate path %s", file, path)
			}
			if defaults, exists := doc["security"]; exists {
				for method, operation := range item.(map[string]any) {
					if !strings.Contains(" get put post delete patch head options trace ", " "+method+" ") {
						continue
					}
					op := operation.(map[string]any)
					if _, explicit := op["security"]; !explicit {
						op["security"] = defaults
					}
				}
			}
			paths[path] = item
		}
	}
	root["paths"], root["components"] = paths, components
	return yaml.Marshal(root)
}
