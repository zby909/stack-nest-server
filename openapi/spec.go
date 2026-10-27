package openapi

import (
	_ "embed"
	"strings"
)

//go:embed openapi.yaml
var routes string

//go:embed components.yaml
var components string

// Document 将分开的路由与模型组装为单一公开契约，客户端无需额外请求引用文件。
var Document = document()

// document 保留原始 YAML 内容，仅合并固定分界并转换同目录引用。
func document() []byte {
	root, _, found := strings.Cut(routes, "\ncomponents:")
	_, models, modelsFound := strings.Cut(components, "\ncomponents:")
	if !found || !modelsFound {
		panic("OpenAPI component boundary is missing")
	}
	root = strings.ReplaceAll(root, "./components.yaml#/components/", "#/components/")
	return []byte(root + "\ncomponents:" + models)
}
