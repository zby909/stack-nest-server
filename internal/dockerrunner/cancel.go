package dockerrunner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"stacknest/server/internal/containerapp"
)

type activeOperation struct {
	id         string
	generation int64
	canCancel  bool
	canceled   bool
	cancel     context.CancelCauseFunc
}

// canceledGeneration 读取每个应用唯一的取消代次；应用容器无法访问该控制目录。
func (r *Runner) canceledGeneration(id string) (int64, error) {
	if r.dataDir == "" {
		return 0, nil
	}
	path := filepath.Join(r.dataDir, "apps", id, "cancel-generation")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32 {
		return 0, errors.New("取消记录读取失败")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, errors.New("取消记录读取失败")
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || value < 1 {
		return 0, errors.New("取消记录格式无效")
	}
	return value, nil
}

// saveCancellation 在确认取消前原子写入并同步磁盘，进程重启后同一代次不会重新部署。
func (r *Runner) saveCancellation(id string, generation int64) error {
	if r.dataDir == "" {
		return containerapp.ErrUnavailable
	}
	// 与配额元数据放在应用数据的父目录，随既有备份迁移，用户只挂载其中的 data 子目录。
	dir := filepath.Join(r.dataDir, "apps", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".cancel-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.WriteString(strconv.FormatInt(generation, 10) + "\n"); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), filepath.Join(dir, "cancel-generation")); err != nil {
		return err
	}
	// 运行器部署在 Linux；Windows 仅用于本地测试，其目录句柄不支持 Sync。
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// beginOperation 注册独立取消上下文；只允许部署在容器切换前接受取消。
func (r *Runner) beginOperation(ctx context.Context, op containerapp.Operation) (context.Context, func(), error) {
	ctx, cancel := context.WithCancelCause(ctx)
	active := &activeOperation{id: op.ID, generation: op.Generation, canCancel: op.Action == "deploy", cancel: cancel}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	if r.activeOperation != nil {
		cancel(nil)
		return ctx, func() {}, containerapp.ErrBusy
	}
	r.activeOperation = active
	finish := func() {
		r.operationMu.Lock()
		if r.activeOperation == active {
			r.activeOperation = nil
		}
		r.operationMu.Unlock()
		cancel(nil)
	}
	if op.Action == "deploy" {
		generation, err := r.canceledGeneration(op.ID)
		if err != nil {
			return ctx, finish, err
		}
		if generation >= op.Generation && generation > 0 {
			cancel(containerapp.ErrDeployCanceled)
			return ctx, finish, containerapp.ErrDeployCanceled
		}
	}
	return ctx, finish, nil
}

// sealOperation 把取消与开始容器切换做成同一个互斥判定，避免取消成功后仍创建或停止容器。
func (r *Runner) sealOperation(ctx context.Context) error {
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if r.activeOperation != nil {
		r.activeOperation.canCancel = false
	}
	if task, ok := ctx.Value(progressKey{}).(progressTask); ok {
		r.progressMu.Lock()
		p := r.progress[task.ID]
		p.CanCancel = false
		r.progress[task.ID] = p
		r.progressMu.Unlock()
	}
	return nil
}

// requestCancellation 独立于长操作锁，仅取消完全匹配的任务，绝不打断其他应用或新代次。
func (r *Runner) requestCancellation(id string, generation int64) error {
	if !containerapp.ValidID(id) || generation < 1 {
		return containerapp.ErrNotFound
	}
	r.operationMu.Lock()
	defer r.operationMu.Unlock()
	previous, err := r.canceledGeneration(id)
	if err != nil {
		return err
	}
	if previous > generation {
		return containerapp.ErrBusy
	}
	active := r.activeOperation
	if active != nil && active.id == id {
		if active.generation != generation {
			return containerapp.ErrBusy
		}
		if active.canceled {
			return nil
		}
		if !active.canCancel {
			return containerapp.ErrCancelTooLate
		}
	}
	r.progressMu.Lock()
	p := r.progress[id]
	r.progressMu.Unlock()
	if previous == generation && p.Generation == generation && (p.Stage == "cancelling" || p.Stage == "cancelled") {
		return nil
	}
	if p.Generation > generation || p.Generation == generation && !p.CanCancel {
		return containerapp.ErrCancelTooLate
	}
	if err = r.saveCancellation(id, generation); err != nil {
		return errors.New("取消请求保存失败，请稍后重试")
	}
	if active != nil && active.id == id && active.generation == generation {
		active.canCancel = false
		active.canceled = true
		active.cancel(containerapp.ErrDeployCanceled)
		r.progressMu.Lock()
		p = r.progress[id]
		p.Stage, p.CanCancel = "cancelling", false
		r.progress[id] = p
		r.progressMu.Unlock()
	}
	return nil
}

// cancelOperation 只接收应用和代次，不接受 Docker 指令、容器编号或用户配置。
func (r *Runner) cancelOperation(w http.ResponseWriter, req *http.Request) {
	var input struct {
		ID         string `json:"id"`
		Generation int64  `json:"generation"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 512))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !containerapp.ValidID(input.ID) || input.Generation < 1 {
		respond(w, 400, map[string]string{"error": "取消参数无效"})
		return
	}
	if err := r.requestCancellation(input.ID, input.Generation); err != nil {
		respond(w, 409, map[string]string{"error": err.Error()})
		return
	}
	respond(w, 200, map[string]bool{"accepted": true})
}
