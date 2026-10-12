package dockerrunner

import (
	"context"
	"net/http"
	"stacknest/server/internal/containerapp"
	"strconv"
	"time"
)

type progressKey struct{}
type progressTask struct {
	ID         string
	Generation int64
}

// beginProgress 重试同一任务时清除旧快照，同时清理过期的历史记录。
func (r *Runner) beginProgress(ctx context.Context, op containerapp.Operation) context.Context {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	if r.progress == nil {
		r.progress = map[string]containerapp.Progress{}
	}
	now := time.Now().UTC()
	for id, p := range r.progress {
		if now.Sub(p.UpdatedAt) > 30*time.Minute {
			delete(r.progress, id)
		}
	}
	if len(r.progress) >= 256 {
		oldest := ""
		var oldestAt time.Time
		for id, p := range r.progress {
			if oldest == "" || p.UpdatedAt.Before(oldestAt) {
				oldest, oldestAt = id, p.UpdatedAt
			}
		}
		delete(r.progress, oldest)
	}
	r.progress[op.ID] = containerapp.Progress{Generation: op.Generation, Stage: "preparing", UpdatedAt: now}
	return context.WithValue(ctx, progressKey{}, progressTask{op.ID, op.Generation})
}

// stage 只更新当前代次，旧任务不能覆盖新的操作状态。
func (r *Runner) stage(ctx context.Context, stage string) {
	task, ok := ctx.Value(progressKey{}).(progressTask)
	if !ok {
		return
	}
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	p := r.progress[task.ID]
	if p.Generation != task.Generation {
		return
	}
	if stage == "restoring" {
		p.FailedStage = p.Stage
	}
	p.Stage, p.UpdatedAt = stage, time.Now().UTC()
	r.progress[task.ID] = p
}

// download 只保留数值汇总，不向管理页面传递仓库事件原文。
func (r *Runner) download(ctx context.Context, current, total int64) {
	task, ok := ctx.Value(progressKey{}).(progressTask)
	if !ok {
		return
	}
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	p := r.progress[task.ID]
	if p.Generation != task.Generation {
		return
	}
	p.DownloadedBytes, p.TotalBytes, p.UpdatedAt = current, total, time.Now().UTC()
	r.progress[task.ID] = p
}

// finishProgress 保存失败发生的阶段；重启后的任务由数据库重新排队。
func (r *Runner) finishProgress(ctx context.Context, err error) {
	task, ok := ctx.Value(progressKey{}).(progressTask)
	if !ok {
		return
	}
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	p := r.progress[task.ID]
	if p.Generation != task.Generation {
		return
	}
	if err != nil {
		if p.FailedStage == "" {
			p.FailedStage = p.Stage
		}
		p.Stage = "failed"
	} else {
		p.Stage, p.FailedStage = "completed", ""
	}
	p.UpdatedAt = time.Now().UTC()
	r.progress[task.ID] = p
}

// readProgress 使用独立锁，长时间拉取镜像不会阻塞查询。
func (r *Runner) readProgress(w http.ResponseWriter, req *http.Request) {
	generation, err := strconv.ParseInt(req.PathValue("generation"), 10, 64)
	id := req.PathValue("id")
	if err != nil || generation < 1 || !containerapp.ValidID(id) {
		respond(w, 404, map[string]string{"error": "当前任务进度不可用"})
		return
	}
	r.progressMu.Lock()
	value, ok := r.progress[id]
	r.progressMu.Unlock()
	if !ok || value.Generation != generation || time.Since(value.UpdatedAt) > 30*time.Minute {
		respond(w, 404, map[string]string{"error": "当前任务进度不可用"})
		return
	}
	respond(w, 200, value)
}

// pullEvent 只读取 Docker 拉取流中需要的字段，原文永不返回浏览器。
type pullEvent struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Error          string `json:"error"`
	ProgressDetail struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
}

type pullLayer struct{ Current, Total int64 }

// recordPull 汇总已知镜像层；新层发现会增加总量，因此它不是整个部署的百分比。
func recordPull(layers map[string]pullLayer, event pullEvent) (int64, int64) {
	_, known := layers[event.ID]
	if event.ID != "" && len(event.ID) <= 128 && (len(layers) < 512 || known) {
		previous := layers[event.ID]
		if event.Status == "Downloading" && event.ProgressDetail.Current >= 0 && event.ProgressDetail.Total > 0 && event.ProgressDetail.Total <= 64<<30 {
			previous = pullLayer{min(event.ProgressDetail.Current, event.ProgressDetail.Total), event.ProgressDetail.Total}
		} else if event.Status == "Download complete" || event.Status == "Pull complete" {
			previous.Current = previous.Total
		}
		layers[event.ID] = previous
	}
	var current, total int64
	for _, layer := range layers {
		current += layer.Current
		total += layer.Total
	}
	return current, total
}
