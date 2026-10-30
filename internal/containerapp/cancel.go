package containerapp

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/activity"
)

// Cancel 先校验归属与代次，再持久化取消请求；排队任务可立即结束，执行中的任务由运行器收尾。
func (s *Service) Cancel(ctx context.Context, id string, actor Actor, generation int64) (App, error) {
	a, err := s.Get(ctx, id, actor)
	if err != nil {
		return a, err
	}
	if generation < 1 || a.Generation != generation || a.Action != "deploy" || (a.Status != "pending" && a.Status != "deploying") {
		return a, ErrBusy
	}
	if err = s.runtime.Cancel(ctx, id, generation); err != nil {
		return a, err
	}
	if a.Status == "pending" {
		if a.ActiveSealed != "" {
			spec, err := open(s.secret, id, a.ActiveSealed)
			if err != nil {
				return a, err
			}
			a.Sealed, a.MemoryMiB = a.ActiveSealed, spec.MemoryMiB
		}
		// 若工作进程已领取，条件更新不生效；运行器仍会识别已保存的取消请求。
		if err = s.store.CancelPendingApp(ctx, a); err != nil {
			return a, err
		}
	}
	if s.activity != nil {
		event := activity.Event{ID: uuid.NewString(), CreatedAt: time.Now().UTC(), Action: "app-cancel", Actor: actor.Email, ActorID: actor.ID, ProjectOwnerID: a.OwnerID, Slug: a.Slug, Status: "succeeded", Message: "已请求取消本次部署"}
		if err := s.activity.Append(event); err != nil {
			slog.Error("append app cancellation", "id", id, "error", err)
		}
	}
	return s.Get(ctx, id, actor)
}

// Cancel 通过独立请求取消指定代次，不关闭操作连接，以便接收准确结果。
func (c *Client) Cancel(ctx context.Context, id string, generation int64) error {
	task, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := c.call(task, "POST", "/cancel", map[string]any{"id": id, "generation": generation}, nil)
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrBusy, ErrCancelTooLate} {
		if err.Error() == known.Error() {
			return known
		}
	}
	return ErrUnavailable
}
