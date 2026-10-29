package dockerrunner

import (
	"context"
	"errors"
	"stacknest/server/internal/containerapp"
	"sync"
	"time"
)

const pullIdleTimeout = 5 * time.Minute

var (
	errPullIdle    = errors.New("镜像拉取超时：连续 5 分钟没有下载或解压进展，请检查仓库连接后重试")
	errPullTimeout = errors.New("镜像拉取超时：已达到 60 分钟上限，请检查下载速度或缩小镜像后重试")
	errPullDisk    = errors.New("Docker 磁盘空间不足，已暂停拉取镜像")
)

// watchPull 分开检查总时长、实际进展和磁盘；取消原因只包含可公开的错误。
func watchPull(ctx context.Context, checkDisk func() error) (context.Context, func(), context.CancelFunc) {
	total, stopTotal := context.WithTimeoutCause(ctx, containerapp.ImagePullTimeout, errPullTimeout)
	task, cancel := context.WithCancelCause(total)
	var mu sync.Mutex
	lastProgress := time.Now()
	progress := func() {
		mu.Lock()
		lastProgress = time.Now()
		mu.Unlock()
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-task.Done():
				return
			case <-ticker.C:
				mu.Lock()
				if time.Since(lastProgress) >= pullIdleTimeout {
					cancel(errPullIdle)
				}
				mu.Unlock()
				if task.Err() != nil {
					return
				}
				if checkDisk() != nil {
					cancel(errPullDisk)
					return
				}
			}
		}
	}()
	return task, progress, func() { cancel(nil); stopTotal() }
}

type pullActivityLayer struct {
	stages                uint8
	downloaded, extracted int64
}

type pullActivity map[string]pullActivityLayer

// observe 仅让新阶段或字节增长续期；重试回退不算进展，但重新下载的字节仍可计入。
func (layers pullActivity) observe(event pullEvent) bool {
	previous, known := layers[event.ID]
	if event.ID == "" || len(event.ID) > 128 || !known && len(layers) >= 512 {
		return false
	}
	var stage uint8
	switch event.Status {
	case "Pulling fs layer":
		stage = 1
	case "Waiting":
		stage = 2
	case "Downloading":
		stage = 4
	case "Verifying Checksum":
		stage = 8
	case "Download complete":
		stage = 16
	case "Extracting":
		stage = 32
	case "Pull complete":
		stage = 64
	case "Already exists":
		stage = 128
	default:
		return false
	}
	advanced := previous.stages&stage == 0
	previous.stages |= stage
	current, total := event.ProgressDetail.Current, event.ProgressDetail.Total
	if current >= 0 && total > 0 && current <= total && total <= 64<<30 {
		if event.Status == "Downloading" {
			advanced = advanced || current > previous.downloaded
			previous.downloaded = current
		} else if event.Status == "Extracting" {
			advanced = advanced || current > previous.extracted
			previous.extracted = current
		}
	}
	layers[event.ID] = previous
	return advanced
}
