package containerapp

import (
	"errors"
	"time"
)

// ImagePullTimeout 给持续推进的慢速下载留足时间，同时限制任务总占用。
const ImagePullTimeout = time.Hour

var ErrOperationTimeout = errors.New("Docker 应用操作超时，请查看部署阶段后重试")

// OperationTimeout 为可能需要拉取镜像的操作预留额外时间，启停和删除仍有界。
func OperationTimeout(action string) time.Duration {
	if action == "deploy" || action == "start" || action == "restart" {
		return ImagePullTimeout + 10*time.Minute
	}
	return 10 * time.Minute
}
