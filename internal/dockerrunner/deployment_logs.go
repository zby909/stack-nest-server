package dockerrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"stacknest/server/internal/containerapp"
)

const failureTextLimit = 256 << 10
const failureFileLimit = 2 << 20

type deploymentFailure struct {
	ID         string `json:"id"`
	Generation int64  `json:"generation"`
	Text       string `json:"text"`
}

// boundedFailureText 限制脱敏后的日志大小，保持 UTF-8 完整，避免短秘密替换造成无限膨胀。
func boundedFailureText(text string, limit int) string {
	text = strings.ToValidUTF8(text, "\ufffd")
	if len(text) <= limit {
		return text
	}
	text = text[:limit-64]
	for !utf8.ValidString(text) && len(text) > 0 {
		text = text[:len(text)-1]
	}
	return text + "\n[失败详情已截断]"
}

// captureDeploymentFailure 在删除候选前保存受限状态和日志，只保留每个应用最近一次失败。
func (r *Runner) captureDeploymentFailure(ctx context.Context, op containerapp.Operation, cid string, cause error) string {
	reason := "容器未通过就绪检查：" + cause.Error()
	stateText, output := "容器状态暂不可用", "容器未产生可读取的启动日志。"
	value, err := r.owned(ctx, op.ID, cid)
	if err == nil {
		stateText = fmt.Sprintf("容器状态：%s\n采样退出码：%d\n重启次数：%d\nOOM：%t", value.State.Status, value.State.ExitCode, value.RestartCount, value.State.OOMKilled)
		if value.State.Error != "" {
			stateText += "\n启动错误：" + boundedFailureText(value.State.Error, 2048)
		}
		switch {
		case value.State.OOMKilled:
			reason = "容器因超出内存限制被终止（OOM）"
		case value.RestartCount > 0:
			reason = fmt.Sprintf("容器反复重启（%d 次），未通过就绪检查，请查看失败详情", value.RestartCount)
		case value.State.Status == "exited" || value.State.Status == "dead":
			reason = fmt.Sprintf("容器进程已退出（退出码 %d），请查看失败详情", value.State.ExitCode)
		case value.State.Error != "":
			reason = "容器启动失败：" + value.State.Error
		}
		response, logErr := r.engine.request(ctx, "GET", "/containers/"+url.PathEscape(cid)+"/logs?stdout=1&stderr=1&timestamps=1&tail=200", nil, nil)
		if logErr == nil {
			text, readErr := readLogs(response.Body)
			response.Body.Close()
			if readErr == nil && text != "" {
				output = text
			} else if readErr != nil {
				output = "启动日志读取失败；请根据容器状态检查应用配置。"
			}
		} else {
			output = "启动日志暂时无法读取。"
		}
	}
	header := fmt.Sprintf("失败时间：%s\n任务代次：%d\n镜像：%s\n容器端口：%d\nHTTP 健康路径：%s\n%s\n原因：%s\n\n启动日志（最多 200 行）：\n", time.Now().UTC().Format(time.RFC3339), op.Generation, op.Spec.Image, op.Spec.Port, op.Spec.HealthPath, stateText, reason)
	record := deploymentFailure{ID: op.ID, Generation: op.Generation, Text: boundedFailureText(containerapp.RedactLog(header+output, op.Spec), failureTextLimit)}
	reason = boundedFailureText(containerapp.RedactLog(reason, op.Spec), 2048)
	if err := r.saveDeploymentFailure(record); err != nil {
		slog.Error("save deployment failure", "id", op.ID, "generation", op.Generation, "error", err)
		return reason + "；失败详情保存失败"
	}
	return reason
}

// saveDeploymentFailure 把已脱敏快照原子保存到用户数据挂载之外，随应用删除和备份迁移。
func (r *Runner) saveDeploymentFailure(record deploymentFailure) error {
	if !containerapp.ValidID(record.ID) || record.Generation < 1 || r.dataDir == "" || len(record.Text) > failureTextLimit {
		return errors.New("失败快照路径或大小无效")
	}
	dir := filepath.Join(r.dataDir, "apps", record.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	body, err := json.Marshal(record)
	if err != nil || len(body) > failureFileLimit {
		return errors.New("失败快照超过大小限制")
	}
	file, err := os.CreateTemp(dir, ".failure-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(body); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "last-deployment-failure.json"))
}

// deploymentLogs 仅返回匹配应用与代次的受限快照，不读取任意宿主文件或其他任务。
func (r *Runner) deploymentLogs(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	generation, err := strconv.ParseInt(req.PathValue("generation"), 10, 64)
	if !containerapp.ValidID(id) || err != nil || generation < 1 {
		respond(w, 404, map[string]string{"error": "失败详情不存在"})
		return
	}
	path := filepath.Join(r.dataDir, "apps", id, "last-deployment-failure.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		respond(w, 404, map[string]string{"error": "失败详情不存在"})
		return
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > failureFileLimit {
		respond(w, 503, map[string]string{"error": "失败详情暂时无法读取"})
		return
	}
	body, err := os.ReadFile(path)
	var record deploymentFailure
	if err != nil || json.Unmarshal(body, &record) != nil || len(record.Text) > failureTextLimit {
		respond(w, 503, map[string]string{"error": "失败详情暂时无法读取"})
		return
	}
	if record.ID != id || record.Generation != generation {
		respond(w, 404, map[string]string{"error": "失败详情不存在"})
		return
	}
	respond(w, 200, map[string]string{"text": record.Text})
}
