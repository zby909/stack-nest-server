package containerapp

import (
	"context"
	"time"
)

// Diagnostics 仅返回安装自检所需能力，不暴露宿主路径、应用环境或 Docker 任意配置。
type Diagnostics struct {
	Driver           string `json:"driver"`
	DataQuota        bool   `json:"dataQuota"`
	WritableQuota    bool   `json:"writableQuota"`
	NetworkProtected bool   `json:"networkProtected"`
}

// Diagnostics 通过现有私有 socket 做只读自检，不给管理后台挂载 Docker socket。
func (c *Client) Diagnostics(ctx context.Context) (Diagnostics, error) {
	task, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var value Diagnostics
	err := c.call(task, "GET", "/diagnostics", nil, &value)
	return value, err
}
