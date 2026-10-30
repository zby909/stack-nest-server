package dockerrunner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"stacknest/server/internal/containerapp"
	"strings"
)

// imageInfo 只读取大小、平台及镜像声明的卷。
type imageInfo struct {
	ID               string `json:"Id"`
	Size             int64
	Os, Architecture string
	Config           struct{ Volumes map[string]any }
}

// pull 在拉取期间持续检查 Docker 文件系统，错误消息不携带仓库凭据。
func (r *Runner) pull(ctx context.Context, spec containerapp.Spec, pinned string) (value imageInfo, pullErr error) {
	r.stage(ctx, "pulling")
	if pinned != "" && strings.HasPrefix(pinned, "sha256:") && len(pinned) == 71 {
		if err := r.engine.call(ctx, "GET", "/images/"+url.PathEscape(pinned)+"/json", nil, &value); err == nil {
			r.stage(ctx, "verifying")
			return value, r.validateImage(value, spec)
		}
	}
	if err := r.guard.Check(2 * containerapp.MaxImageBytes); err != nil {
		return value, errPullDisk
	}
	task, progress, cancel := watchPull(ctx, func() error { return r.guard.Check(128 << 20) })
	defer func() {
		if cause := context.Cause(task); cause != nil {
			pullErr = cause
		}
		cancel()
	}()
	headers := map[string]string{}
	if spec.Registry != nil {
		head, _, found := strings.Cut(spec.Image, "/")
		host := "https://index.docker.io/v1/"
		if found && strings.ContainsAny(head, ".:") {
			host = head
		}
		raw, _ := json.Marshal(map[string]string{"username": spec.Registry.Username, "password": spec.Registry.Password, "serveraddress": host})
		headers["X-Registry-Auth"] = base64.URLEncoding.EncodeToString(raw)
	}
	response, err := r.engine.request(task, "POST", "/images/create?fromImage="+url.QueryEscape(spec.Image), nil, headers)
	if err != nil {
		return value, errors.New("镜像拉取失败，请检查镜像地址、仓库权限与网络")
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(response.Body)
	layers := map[string]pullLayer{}
	activity := pullActivity{}
	for {
		var event pullEvent
		if err = decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			return value, errors.New("镜像拉取中断，请检查仓库连接后重试")
		}
		if activity.observe(event) {
			progress()
		}
		current, total := recordPull(layers, event)
		r.download(ctx, current, total)
		if event.Error != "" {
			return value, errors.New("镜像拉取失败，请确认 Linux 镜像、服务器架构和仓库权限")
		}
	}
	if cause := context.Cause(task); cause != nil {
		return value, cause
	}
	r.stage(ctx, "verifying")
	if err = r.engine.call(task, "GET", "/images/"+url.PathEscape(spec.Image)+"/json", nil, &value); err != nil {
		return value, err
	}
	return value, r.validateImage(value, spec)
}

// validateImage 禁止多个隐式匿名数据卷，并限制拉取后的镜像体积。
func (r *Runner) validateImage(value imageInfo, spec containerapp.Spec) error {
	if value.Size > containerapp.MaxImageBytes {
		return errors.New("镜像解压后超过 1 GiB，暂不部署")
	}
	if value.Os != "linux" || value.Architecture != "amd64" {
		return errors.New("当前服务器需要 Linux amd64 镜像")
	}
	for dir := range value.Config.Volumes {
		if dir != spec.DataPath {
			return errors.New("镜像声明了额外数据卷，请使用仅含一个数据卷的镜像并填写对应目录")
		}
	}
	return nil
}
