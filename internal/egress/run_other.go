//go:build !linux

package egress

import (
	"context"
	"errors"
)

// Run 明确拒绝在非 Linux 系统安装宿主网络规则。
func Run(context.Context, []string) error {
	return errors.New("宿主网络规则服务仅支持 Linux")
}
