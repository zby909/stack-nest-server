package openapi

//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 --config oapi-codegen.yaml openapi.yaml
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 --config oapi-server.yaml openapi.yaml
//go:generate go run ../scripts/split-generated.go
