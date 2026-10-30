package dockerrunner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"stacknest/server/internal/containerapp"
	"strconv"
	"time"
)

// current 返回本应用当前容器，优先选择仍在运行的版本。
func (r *Runner) current(ctx context.Context, id string) (inspection, error) {
	values, err := r.containers(ctx, id)
	if err != nil {
		return inspection{}, err
	}
	sort.Slice(values, func(i, j int) bool {
		if (values[i].State == "running") != (values[j].State == "running") {
			return values[i].State == "running"
		}
		left, _ := strconv.ParseInt(values[i].Labels[generationLabel], 10, 64)
		right, _ := strconv.ParseInt(values[j].Labels[generationLabel], 10, 64)
		return left > right
	})
	for _, value := range values {
		if found, err := r.owned(ctx, id, value.ID); err == nil {
			return found, nil
		}
	}
	return inspection{}, errMissing
}

// operate 仅对本应用执行操作，启动失败尽力恢复旧容器。
func (r *Runner) operate(ctx context.Context, op containerapp.Operation) (output result, operationErr error) {
	ctx, cancel := context.WithTimeoutCause(ctx, containerapp.OperationTimeout(op.Action), containerapp.ErrOperationTimeout)
	defer cancel()
	ctx = r.beginProgress(ctx, op)
	ctx, finish, beginErr := r.beginOperation(ctx, op)
	defer finish()
	defer func() {
		if cause := context.Cause(ctx); cause != nil {
			operationErr = cause
		}
		r.finishProgress(ctx, operationErr)
	}()
	if beginErr != nil {
		return result{}, beginErr
	}
	if op.Action == "deploy" || op.Action == "start" || op.Action == "restart" {
		if err := r.checkEgress(); err != nil {
			return result{}, err
		}
	}
	old, oldErr := r.current(ctx, op.ID)
	fallback := result{State: "missing"}
	if oldErr == nil {
		fallback = stateResponse(old)
	} else if !errors.Is(oldErr, errMissing) {
		return fallback, oldErr
	}
	switch op.Action {
	case "deploy":
		return r.deploy(ctx, op, old)
	case "start":
		r.stage(ctx, "starting")
		if oldErr != nil {
			return r.deploy(ctx, op, old)
		}
		if !old.State.Running {
			if err := r.start(ctx, old.ID); err != nil {
				return fallback, err
			}
		}
	case "stop":
		r.stage(ctx, "stopping")
		if oldErr != nil {
			return fallback, nil
		}
		if old.State.Running {
			if err := r.stop(ctx, old.ID); err != nil {
				return fallback, err
			}
		}
	case "restart":
		r.stage(ctx, "starting")
		if oldErr != nil {
			return r.deploy(ctx, op, old)
		}
		if err := r.engine.call(ctx, "POST", "/containers/"+old.ID+"/restart?t=15", nil, nil); err != nil {
			return fallback, err
		}
	case "delete":
		r.stage(ctx, "deleting")
		return r.delete(ctx, op.ID)
	}
	value, err := r.owned(ctx, op.ID, old.ID)
	if err != nil {
		return fallback, err
	}
	if op.Action == "start" || op.Action == "restart" {
		if err = r.ready(ctx, op.ID, value.ID); err != nil {
			return stateResponse(value), err
		}
	}
	return stateResponse(value), nil
}

// checkQuota 在运行器再次检查资源，即使后台配置错误也不触碰平台容器。
func (r *Runner) checkQuota(ctx context.Context, id string, memory int, limits containerapp.Limits) error {
	values, err := r.containers(ctx, "")
	if err != nil {
		return err
	}
	allocations := map[string]int64{}
	for _, summary := range values {
		appID := summary.Labels[appLabel]
		if appID == id {
			continue
		}
		value, err := r.owned(ctx, appID, summary.ID)
		if err != nil {
			return err
		}
		if value.HostConfig.Memory > allocations[appID] {
			allocations[appID] = value.HostConfig.Memory
		}
	}
	total := int64(memory) << 20
	for _, value := range allocations {
		total += value
	}
	if len(allocations) >= limits.MaxApps || total > int64(limits.TotalMemoryMiB)<<20 {
		return containerapp.ErrQuota
	}
	return nil
}

// create 将用户输入转换为固定的非特权容器配置，不开放宿主端口。
func (r *Runner) create(ctx context.Context, op containerapp.Operation, image imageInfo) (string, error) {
	mounts, err := r.bind(op.ID, op.Spec)
	if err != nil {
		return "", err
	}
	env := []string{}
	for key, value := range op.Spec.Env {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	security := []string{"no-new-privileges:true"}
	if r.quota != nil {
		security = append(security, "seccomp="+quotaSeccomp)
	}
	payload := map[string]any{
		"Tty": false, "Image": image.ID, "Env": env, "Labels": map[string]string{instanceLabel: r.instance, appLabel: op.ID, portLabel: strconv.Itoa(op.Spec.Port), generationLabel: strconv.FormatInt(op.Generation, 10), healthPathLabel: op.Spec.HealthPath},
		"HostConfig": map[string]any{
			"NetworkMode": r.networkName(op.ID), "Memory": int64(op.Spec.MemoryMiB) << 20, "MemorySwap": int64(op.Spec.MemoryMiB) << 20,
			"NanoCpus": int64(op.Spec.CPUs * 1e9), "PidsLimit": 128, "Init": true, "Privileged": false,
			"SecurityOpt": security, "CapDrop": []string{"NET_RAW", "MKNOD", "SYS_CHROOT", "SETPCAP", "AUDIT_WRITE", "SETFCAP"},
			"Mounts": mounts, "Tmpfs": map[string]string{"/tmp": "rw,nosuid,nodev,size=16777216"},
			"RestartPolicy": map[string]any{"Name": "unless-stopped"},
			"LogConfig":     map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "5m", "max-file": "2"}},
		},
	}
	if r.egressFile != "" {
		status, err := readEgressStatus(r.egressFile)
		if err != nil {
			return "", err
		}
		payload["HostConfig"].(map[string]any)["Dns"] = status.DNSServers
	}
	if r.quota != nil {
		payload["HostConfig"].(map[string]any)["StorageOpt"] = map[string]string{"size": "1G"}
	}
	var response struct {
		ID string `json:"Id"`
	}
	err = r.engine.call(ctx, "POST", "/containers/create?name="+url.QueryEscape(r.candidateName(op.ID, op.Generation)), payload, &response)
	return response.ID, err
}

// deploy 先拉取和创建候选容器，再切换；候选不健康时恢复旧容器。
func (r *Runner) deploy(ctx context.Context, op containerapp.Operation, old inspection) (result, error) {
	fallback := result{State: "missing"}
	if old.ID != "" {
		fallback = stateResponse(old)
	}
	limits, err := r.operationLimits(op)
	if err != nil {
		return fallback, err
	}
	if err := r.checkQuota(ctx, op.ID, op.Spec.MemoryMiB, limits); err != nil {
		return fallback, err
	}
	if err := r.ensureNetwork(ctx, op.ID); err != nil {
		return fallback, err
	}
	candidate, err := r.engine.inspect(ctx, r.candidateName(op.ID, op.Generation))
	if err == nil {
		if err = r.sealOperation(ctx); err != nil {
			return fallback, err
		}
		if _, err = r.owned(ctx, op.ID, candidate.ID); err != nil {
			return fallback, errors.New("候选容器归属异常")
		}
		if candidate.State.Running && r.ready(ctx, op.ID, candidate.ID) == nil {
			r.cleanOthers(ctx, op.ID, candidate.ID)
			return stateResponse(candidate), nil
		}
		if candidate.ID == old.ID {
			old = inspection{}
		}
		if err = r.remove(ctx, candidate.ID); err != nil {
			return fallback, err
		}
	} else if !errors.Is(err, errMissing) {
		return fallback, err
	}
	pinned := ""
	if op.Action == "start" || op.Action == "restart" {
		pinned = op.ImageID
	}
	image, err := r.pull(ctx, op.Spec, pinned)
	if err != nil {
		return fallback, err
	}
	if err = r.sealOperation(ctx); err != nil {
		return fallback, err
	}
	r.stage(ctx, "creating")
	cid, err := r.create(ctx, op, image)
	if err != nil {
		return fallback, err
	}
	// 旧容器退出后才启动新容器，避免共享数据目录并发写入。
	if old.ID != "" && old.State.Running {
		r.stage(ctx, "stopping")
		if err = r.stop(ctx, old.ID); err != nil {
			_ = r.remove(ctx, cid)
			return fallback, err
		}
	}
	r.stage(ctx, "starting")
	err = r.start(ctx, cid)
	if err == nil {
		err = r.ready(ctx, op.ID, cid)
	}
	if err != nil {
		r.stage(ctx, "restoring")
		recoverCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		detailCtx, detailCancel := context.WithTimeout(recoverCtx, 5*time.Second)
		reason := r.captureDeploymentFailure(detailCtx, op, cid, err)
		detailCancel()
		_ = r.remove(recoverCtx, cid)
		if old.ID != "" && old.State.Running {
			if restoreErr := r.start(recoverCtx, old.ID); restoreErr != nil {
				return result{ContainerID: old.ID, ImageID: old.Image, State: "exited"}, errors.New("新版本启动失败，旧容器恢复失败，请检查日志")
			}
		}
		return fallback, fmt.Errorf("%s；已尝试恢复原有容器（如有）；持久化数据不随容器恢复", reason)
	}
	value, err := r.owned(ctx, op.ID, cid)
	if err != nil {
		return fallback, err
	}
	r.stage(ctx, "cleaning")
	r.cleanOthers(ctx, op.ID, cid)
	return stateResponse(value), nil
}

// ready 等待私有端口或 HTTP 健康路径就绪，不请求用户指定的外部地址。
func (r *Runner) ready(ctx context.Context, id, cid string) error {
	r.stage(ctx, "healthchecking")
	task, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var lastProbe error
	for {
		value, err := r.owned(task, id, cid)
		if err != nil {
			if task.Err() != nil && ctx.Err() == nil && lastProbe != nil {
				return fmt.Errorf("60 秒内未就绪：%w", lastProbe)
			}
			return err
		}
		if value.State.OOMKilled || value.State.Status == "dead" {
			return errors.New("容器被终止")
		}
		if value.State.Running {
			if lastProbe = r.probe(task, value, id); lastProbe == nil {
				return nil
			}
		}
		select {
		case <-task.Done():
			if lastProbe != nil && ctx.Err() == nil {
				return fmt.Errorf("60 秒内未就绪：%w", lastProbe)
			}
			return task.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// address 仅取本应用网络 IP 和平台写入的端口标签。
func (r *Runner) address(value inspection, id string) (string, error) {
	port, err := strconv.Atoi(value.Config.Labels[portLabel])
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("应用端口无效")
	}
	network, ok := value.NetworkSettings.Networks[r.networkName(id)]
	ip := net.ParseIP(network.IPAddress)
	if !ok || ip == nil || ip.IsLoopback() {
		return "", errors.New("应用网络尚未就绪")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}

// cleanOthers 只删除本应用的旧容器，Docker 镜像与其他服务不做全局清理。
func (r *Runner) cleanOthers(ctx context.Context, id, current string) {
	values, err := r.containers(ctx, id)
	if err != nil {
		return
	}
	for _, value := range values {
		if value.ID == current {
			continue
		}
		if _, err = r.owned(ctx, id, value.ID); err == nil {
			if err = r.remove(ctx, value.ID); err != nil {
				slog.Error("remove old application container", "id", id, "error", err)
			}
		}
	}
}

// delete 删除本应用的全部容器、私有网络与明确的数据目录。
func (r *Runner) delete(ctx context.Context, id string) (result, error) {
	values, err := r.containers(ctx, id)
	if err != nil {
		return result{}, err
	}
	for _, value := range values {
		if _, err = r.owned(ctx, id, value.ID); err != nil {
			return result{}, err
		}
		if err = r.remove(ctx, value.ID); err != nil {
			return result{}, err
		}
	}
	if err = r.deleteNetwork(ctx, id); err != nil {
		return result{}, err
	}
	if !containerapp.ValidID(id) {
		return result{}, fmt.Errorf("应用编号无效")
	}
	dir := filepath.Join(r.dataDir, "apps", id)
	if r.quota != nil {
		err = r.quota.Remove(id, dir)
	} else {
		err = os.RemoveAll(dir)
	}
	if err != nil {
		return result{}, errors.New("应用数据清理失败")
	}
	return result{State: "deleted"}, nil
}
