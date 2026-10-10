//go:build !linux || (!amd64 && !arm64)

package dockerrunner

import "errors"

// newDataQuota 在不支持的系统上拒绝显式启用，不能静默降为软限制。
func newDataQuota(_, _, _ string, _ int64) (dataQuota, error) {
	return nil, errors.New("需要 Linux amd64/arm64、内核 5.14+ 和启用 prjquota 的 XFS")
}
