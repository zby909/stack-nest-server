package staticdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"stacknest/server/internal/project"
)

// RecoverRevisions 按数据库部署编号决定完成切换还是恢复原版，拒绝猜测未知状态。
func (d *Deployer) RecoverRevisions(ctx context.Context, lookup func(context.Context, string) (project.Project, error)) error {
	entries, err := os.ReadDir(filepath.Join(d.config.DataDir, "changes"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || !project.ValidSlug(entry.Name()) {
			return fmt.Errorf("invalid revision journal directory")
		}
		r := d.revision(entry.Name())
		content, err := os.ReadFile(filepath.Join(r.work, "journal.json"))
		if errors.Is(err, os.ErrNotExist) {
			// 没有意图标记时只能有尚未公开的新包，或清理结束留下的空目录。
			children, err := os.ReadDir(r.work)
			if err != nil {
				return err
			}
			for _, child := range children {
				if child.Name() != "next" && child.Name() != "journal.tmp" {
					return fmt.Errorf("revision %q has files but no journal; preserve for recovery", entry.Name())
				}
			}
			if err := os.RemoveAll(r.work); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal(content, &r.journal); err != nil || r.journal.ProjectID == "" || (r.journal.FromID == "" && r.journal.DraftID == "") || r.journal.ToID == "" || r.journal.FromID == r.journal.ToID {
			return fmt.Errorf("invalid revision journal for %q", entry.Name())
		}
		value, err := lookup(ctx, entry.Name())
		if err != nil {
			return fmt.Errorf("verify revision %q: %w", entry.Name(), err)
		}
		if value.ID != r.journal.ProjectID {
			return fmt.Errorf("revision project identity mismatch; preserve journal")
		}
		if r.journal.DraftID != "" && (!validVersionID(r.journal.DraftID) || r.journal.DraftID != r.journal.ToID) {
			return fmt.Errorf("invalid draft journal")
		}
		switch project.CurrentID(value) {
		case r.journal.ToID:
			err = r.Commit()
		case r.journal.FromID:
			err = r.Rollback()
		default:
			return fmt.Errorf("revision deployment mismatch; preserve journal")
		}
		if err != nil {
			return fmt.Errorf("recover revision %q: %w", entry.Name(), err)
		}
		slog.InfoContext(ctx, "interrupted revision recovered", "slug", entry.Name(), "committed", project.CurrentID(value) == r.journal.ToID)
	}
	return nil
}
