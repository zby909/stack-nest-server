package openapi

import _ "embed"

// Document 是 StackNest 对外提供的 OpenAPI 3.1 接口契约。
//
//go:embed openapi.yaml
var Document []byte
