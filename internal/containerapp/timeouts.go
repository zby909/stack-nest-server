package containerapp

import (
	"errors"
	"time"
)

// ImagePullTimeout 给持续推进的慢速下载留足时间，同时限制任务总占用。
const ImagePullTimeout = time.Hour

// StartupTimeout 为旧配置保留 60 秒默认值，调用前由 Spec.Validate 校验上限。
func (s Spec) StartupTimeout() time.Duration {
	if s.StartupTimeoutSeconds == 0 {
		return time.Minute
	}
	return time.Duration(s.StartupTimeoutSeconds) * time.Second
}

var ErrOperationTimeout = errors.New("Docker 应用操作超时，请查看部署阶段后重试")

// OperationTimeout 为可能需要拉取镜像的操作预留额外时间，启停和删除仍有界。
func OperationTimeout(action string) time.Duration {
	if action == "deploy" || action == "start" || action == "restart" {
		return ImagePullTimeout + 10*time.Minute
	}
	return 10 * time.Minute
}
