package containerapp

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// DeploymentLogs 返回当前代次的失败快照，候选配置在恢复旧版前已由运行器脱敏。
func (s *Service) DeploymentLogs(ctx context.Context, a App) (string, error) {
	text, err := s.runtime.DeploymentLogs(ctx, a.ID, a.Generation)
	if errors.Is(err, ErrNotFound) {
		return a.Error + "\n\n本次没有保存容器启动日志：可能在创建容器前失败，或发生在支持失败快照的版本上线之前。", nil
	}
	if err != nil {
		return "", err
	}
	current, err := open(s.secret, a.ID, a.Sealed)
	if err != nil {
		return "", err
	}
	specs := []Spec{current}
	if a.ActiveSealed != "" && a.ActiveSealed != a.Sealed {
		active, err := open(s.secret, a.ID, a.ActiveSealed)
		if err != nil {
			return "", err
		}
		specs = append(specs, active)
	}
	return RedactLog(text, specs...), nil
}

// DeploymentLogs 查询应用和确切代次，不接受浏览器传入容器编号或文件路径。
func (c *Client) DeploymentLogs(ctx context.Context, id string, generation int64) (string, error) {
	task, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var value struct {
		Text string `json:"text"`
	}
	err := c.call(task, "GET", "/deployment-logs/"+id+"/"+strconv.FormatInt(generation, 10), nil, &value)
	return value.Text, err
}
