// Package skills 分发随平台版本提供的通用 Skill，不读取实例或用户的本机配置。
package skills

import (
	"archive/zip"
	"bytes"
	"embed"
	"sync"
)

// 只嵌入明确列出的公共文件，避免把日后新增的本机配置或凭据带入下载包。
//
//go:embed stacknest-deploy/SKILL.md stacknest-deploy/agents/openai.yaml stacknest-deploy/references/local-config.md stacknest-deploy/LICENSE stacknest-deploy/NOTICE
var source embed.FS

var bundle = sync.OnceValues(buildArchive)

// Archive 返回只读的通用安装包；多次下载复用同一份内容。
func Archive() ([]byte, error) { return bundle() }

// buildArchive 使用固定路径生成可直接解压安装的 Skill 目录。
func buildArchive() ([]byte, error) {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range []string{
		"stacknest-deploy/SKILL.md",
		"stacknest-deploy/agents/openai.yaml",
		"stacknest-deploy/references/local-config.md",
		"stacknest-deploy/LICENSE",
		"stacknest-deploy/NOTICE",
	} {
		data, err := source.ReadFile(name)
		if err != nil {
			return nil, err
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o644)
		file, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := file.Write(data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
