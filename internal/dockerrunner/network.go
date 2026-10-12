package dockerrunner

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"stacknest/server/internal/containerapp"
)

// networkName 为每个应用创建独立、不可冲突的 Docker 私有网络。
func (r *Runner) networkName(id string) string { return "stacknest-app-" + r.instance + "-" + id }

// ensureNetwork 保留应用独立网络，仅在宿主规则就绪时开启受控公网出站。
func (r *Runner) ensureNetwork(ctx context.Context, id string) error {
	if err := r.checkEgress(); err != nil {
		return err
	}
	name := r.networkName(id)
	internal := r.egressFile == ""
	options := map[string]string{}
	if !internal {
		options["com.docker.network.bridge.name"] = r.bridgeName(id)
	}
	var value struct {
		Driver   string
		Internal bool
		Labels   map[string]string
		Options  map[string]string
	}
	err := r.engine.call(ctx, "GET", "/networks/"+name, nil, &value)
	if errors.Is(err, errMissing) {
		err = r.engine.call(ctx, "POST", "/networks/create", map[string]any{"Name": name, "Driver": "bridge", "Internal": internal, "Options": options, "Labels": map[string]string{instanceLabel: r.instance, appLabel: id}}, nil)
	} else if err == nil && (value.Driver != "bridge" || value.Internal != internal || (!internal && value.Options["com.docker.network.bridge.name"] != r.bridgeName(id)) || value.Labels[instanceLabel] != r.instance || value.Labels[appLabel] != id) {
		return errors.New("应用网络归属或隔离配置异常")
	}
	if err != nil {
		return err
	}
	self, err := r.engine.inspect(ctx, r.selfID)
	if err != nil {
		return err
	}
	if _, ok := self.NetworkSettings.Networks[name]; ok {
		return nil
	}
	payload := map[string]any{"Container": r.selfID}
	if !internal {
		// 应用网关优先级低于平台网络，避免运行器拉镜像的默认出口被改变。
		payload["EndpointConfig"] = map[string]any{"GwPriority": -1}
	}
	return r.engine.call(ctx, "POST", "/networks/"+name+"/connect", payload, nil)
}

// bind 准备唯一允许的应用目录，禁止挂载平台、主机或其他项目目录。
func (r *Runner) bind(id string, spec containerapp.Spec) ([]map[string]any, error) {
	if !containerapp.ValidID(id) {
		return nil, errors.New("应用编号无效")
	}
	if spec.DataPath == "" {
		return nil, nil
	}
	dir := filepath.Join(r.dataDir, "apps", id, "data")
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
		return nil, errors.New("应用数据目录状态异常")
	}
	if err := os.MkdirAll(dir, 0777); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0777); err != nil {
		return nil, err
	}
	if r.quota != nil {
		if err := r.quota.Ensure(id, dir); err != nil {
			return nil, err
		}
	}
	return []map[string]any{{"Type": "bind", "Source": filepath.Join(r.source, "apps", id, "data"), "Target": spec.DataPath}}, nil
}

// deleteNetwork 只清理经过标签确认的网络，不断开平台的 Compose 网络。
func (r *Runner) deleteNetwork(ctx context.Context, id string) error {
	name := r.networkName(id)
	var value struct{ Labels map[string]string }
	err := r.engine.call(ctx, "GET", "/networks/"+name, nil, &value)
	if errors.Is(err, errMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	if value.Labels[instanceLabel] != r.instance || value.Labels[appLabel] != id {
		return errors.New("应用网络归属异常")
	}
	self, err := r.engine.inspect(ctx, r.selfID)
	if err != nil {
		return err
	}
	if _, ok := self.NetworkSettings.Networks[name]; ok {
		if err = r.engine.call(ctx, "POST", "/networks/"+name+"/disconnect", map[string]any{"Container": r.selfID, "Force": false}, nil); err != nil {
			return err
		}
	}
	return r.engine.call(ctx, "DELETE", "/networks/"+url.PathEscape(name), nil, nil)
}
