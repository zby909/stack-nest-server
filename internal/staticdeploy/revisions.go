package staticdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"stacknest/server/internal/project"
)

type revisionJournal struct {
	DraftID   string `json:"draftId,omitempty"`
	ProjectID string `json:"projectId"`
	FromID    string `json:"fromId"`
	ToID      string `json:"toId"`
	Rollback  bool   `json:"rollback"`
}

type stagedRevision struct {
	journal  revisionJournal
	work     string
	current  string
	previous string
	drafts   string
	stats    project.PublishedFiles
}

// revision 定位公开站点、私有上一版与可恢复的事务目录。
func (d *Deployer) revision(slug string) *stagedRevision {
	return &stagedRevision{
		drafts:   filepath.Join(d.config.DataDir, "drafts", slug),
		work:     filepath.Join(d.config.DataDir, "changes", slug),
		current:  filepath.Join(d.config.DataDir, "sites", slug),
		previous: filepath.Join(d.config.DataDir, "previous", slug),
	}
}

// ensureSettled 阻止新操作覆盖尚未确认的文件事务。
func (d *Deployer) ensureSettled(slug string) error {
	if !project.ValidSlug(slug) {
		return project.ErrInvalidSlug
	}
	for _, directory := range []string{"changes", "pending", "trash"} {
		if _, err := os.Lstat(filepath.Join(d.config.DataDir, directory, slug)); err == nil {
			return project.ErrReleaseBusy
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// StageRevision 在新包验证完成后切换目录，保留旧版和事务标记直到数据库结果确认。
func (d *Deployer) StageRevision(ctx context.Context, input project.RevisionInput) (project.Revision, error) {
	if err := d.ensureSettled(input.Slug); err != nil {
		return nil, err
	}
	r := d.revision(input.Slug)
	if input.ProjectID == "" || (input.FromID == "" && input.DraftID == "") || input.ToID == "" || input.FromID == input.ToID {
		return nil, project.ErrVersionConflict
	}
	if input.FromID != "" && !isRegularFile(filepath.Join(r.current, "index.html")) {
		return nil, fmt.Errorf("current release files missing")
	}
	if input.FromID == "" {
		if _, err := os.Lstat(r.current); !errors.Is(err, os.ErrNotExist) {
			return nil, project.ErrVersionConflict
		}
	}
	var root string
	if input.Rollback {
		if !isRegularFile(filepath.Join(r.previous, "index.html")) {
			return nil, project.ErrNoPrevious
		}
		stats, err := measureSite(ctx, r.previous)
		if err != nil {
			return nil, err
		}
		r.stats = stats
	} else if input.DraftID != "" {
		if !validVersionID(input.DraftID) || input.DraftID != input.ToID {
			return nil, project.ErrVersionConflict
		}
		stats, err := d.MeasureAt(ctx, input.Slug, input.DraftID)
		if err != nil {
			return nil, err
		}
		r.stats = stats
	} else {
		if input.Source == nil {
			return nil, project.NewInvalidArchiveError("请选择 ZIP、RAR 或 HTML 文件")
		}
		prepared, tempDir, stats, err := d.prepareSite(ctx, input.FileName, input.Source)
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tempDir)
		root, r.stats = prepared, stats
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(r.work), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(r.previous), 0o700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(r.work, 0o700); err != nil {
		return nil, err
	}
	if err := syncDirectory(filepath.Dir(r.work)); err != nil {
		_ = os.Remove(r.work)
		return nil, err
	}
	if !input.Rollback && input.DraftID == "" {
		if err := moveDirectory(root, filepath.Join(r.work, "next")); err != nil {
			_ = os.RemoveAll(r.work) // 尚未写入标记，更未触碰线上和上一版。
			return nil, err
		}
	}
	r.journal = revisionJournal{DraftID: input.DraftID, ProjectID: input.ProjectID, FromID: input.FromID, ToID: input.ToID, Rollback: input.Rollback}
	if err := r.writeJournal(); err != nil {
		_ = os.RemoveAll(r.work)
		return nil, err
	}
	if err := r.switchFiles(); err != nil {
		if restoreErr := r.Rollback(); restoreErr != nil {
			return nil, fmt.Errorf("switch release: %w; restore: %v", err, restoreErr)
		}
		return nil, err
	}
	return r, nil
}

// writeJournal 先持久化意图，再改动公开目录。
func (r *stagedRevision) writeJournal() error {
	file, err := os.OpenFile(filepath.Join(r.work, "journal.tmp"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(file).Encode(r.journal)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return moveDirectory(filepath.Join(r.work, "journal.tmp"), filepath.Join(r.work, "journal.json"))
}

// switchFiles 只执行同卷重命名；短暂的目录切换窗口不包含上传和解压时间。
func (r *stagedRevision) switchFiles() error {
	if r.journal.DraftID != "" {
		if err := moveDirectory(filepath.Join(r.drafts, r.journal.DraftID), filepath.Join(r.work, "next")); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(r.current), 0o755); err != nil {
		return err
	}
	if r.journal.Rollback {
		if err := moveDirectory(r.previous, filepath.Join(r.work, "next")); err != nil {
			return err
		}
	} else if err := moveIfExists(r.previous, filepath.Join(r.work, "previous")); err != nil {
		return err
	}
	if r.journal.FromID != "" {
		if err := moveDirectory(r.current, filepath.Join(r.work, "old")); err != nil {
			return err
		}
	}
	return moveDirectory(filepath.Join(r.work, "next"), r.current)
}

// Files 返回本次上线内容的实际体积与文件数。
func (r *stagedRevision) Files() project.PublishedFiles { return r.stats }

// Commit 保留刚被替换的当前版，最后删除意图标记，支持启动时重复执行。
func (r *stagedRevision) Commit() error {
	if !isRegularFile(filepath.Join(r.current, "index.html")) {
		return fmt.Errorf("committed release has no index; preserve journal")
	}
	if _, err := os.Lstat(filepath.Join(r.work, "next")); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("unexpected next release while committing; preserve journal")
	}
	if err := moveIfExists(filepath.Join(r.work, "old"), r.previous); err != nil {
		return err
	}
	if r.journal.FromID != "" && !isRegularFile(filepath.Join(r.previous, "index.html")) {
		return fmt.Errorf("previous release missing; preserve journal")
	}
	if err := os.RemoveAll(filepath.Join(r.work, "previous")); err != nil {
		return err
	}
	return r.clearJournal()
}

// Rollback 恢复切换前的两版文件，恢复过程本身也可以在重启后继续。
func (r *stagedRevision) Rollback() error {
	old := filepath.Join(r.work, "old")
	next := filepath.Join(r.work, "next")
	if r.journal.FromID == "" {
		if err := moveIfExists(r.current, next); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(old); err == nil {
		if err := moveIfExists(r.current, next); err != nil {
			return err
		}
		if err := moveDirectory(old, r.current); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if r.journal.FromID != "" && !isRegularFile(filepath.Join(r.current, "index.html")) {
		return fmt.Errorf("original release missing; preserve journal")
	}
	if r.journal.Rollback {
		if err := moveIfExists(next, r.previous); err != nil {
			return err
		}
	} else {
		if err := moveIfExists(filepath.Join(r.work, "previous"), r.previous); err != nil {
			return err
		}
		if r.journal.DraftID != "" {
			if err := moveIfExists(next, filepath.Join(r.drafts, r.journal.DraftID)); err != nil {
				return err
			}
		} else if err := os.RemoveAll(next); err != nil {
			return err
		}
	}
	return r.clearJournal()
}

// clearJournal 在文件状态稳定后删除标记，避免半清理目录被误当作新操作。
func (r *stagedRevision) clearJournal() error {
	if err := os.Remove(filepath.Join(r.work, "journal.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(r.work); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(r.work))
}

// moveIfExists 允许恢复操作跳过已完成的重命名。
func moveIfExists(from, to string) error {
	if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return moveDirectory(from, to)
}

// moveDirectory 拒绝覆盖已存在目标，并同步目录项以缩小崩溃恢复窗口。
func moveDirectory(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("destination already exists: %s", to)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(from)); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(to))
}
