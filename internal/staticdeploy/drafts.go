package staticdeploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"stacknest/server/internal/project"
)

// validVersionID 限制版本目录和预览子域名只能使用服务器生成的 UUID。
func validVersionID(id string) bool {
	if !strings.HasPrefix(id, "dep-") {
		return false
	}
	parsed, err := uuid.Parse(strings.TrimPrefix(id, "dep-"))
	return err == nil && "dep-"+parsed.String() == id
}

// versionDirectory 将可信版本槽映射到固定的数据目录。
func (d *Deployer) versionDirectory(slug, slot string) (string, error) {
	if err := d.ensureSettled(slug); err != nil {
		return "", err
	}
	if slot == "sites" || slot == "previous" {
		return filepath.Join(d.config.DataDir, slot, slug), nil
	}
	if !validVersionID(slot) {
		return "", project.ErrVersionConflict
	}
	return filepath.Join(d.config.DataDir, "drafts", slug, slot), nil
}

// PrepareDraft 按正式上传的限制验证解压，但只写入不可直接公开的私有目录。
func (d *Deployer) PrepareDraft(ctx context.Context, slug, id, fileName string, source io.Reader) (project.PublishedFiles, error) {
	destination, err := d.versionDirectory(slug, id)
	if err != nil || !validVersionID(id) {
		return project.PublishedFiles{}, project.ErrVersionConflict
	}
	root, tempDir, stats, err := d.prepareSite(ctx, fileName, source)
	if err != nil {
		return project.PublishedFiles{}, err
	}
	defer os.RemoveAll(tempDir)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return project.PublishedFiles{}, err
	}
	if err := moveDirectory(root, destination); err != nil {
		return project.PublishedFiles{}, err
	}
	return stats, nil
}

// RemoveDraft 删除确定不再使用的草稿，严格验证路径片段。
func (d *Deployer) RemoveDraft(slug, id string) error {
	if !project.ValidSlug(slug) || !validVersionID(id) {
		return project.ErrVersionConflict
	}
	return os.RemoveAll(filepath.Join(d.config.DataDir, "drafts", slug, id))
}

// RecoverDrafts 仅清理数据库已确定不再引用的草稿，数据库不可用时保留文件。
func (d *Deployer) RecoverDrafts(ctx context.Context, lookup func(context.Context, string) (project.Project, error)) error {
	root := filepath.Join(d.config.DataDir, "drafts")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !project.ValidSlug(entry.Name()) {
			return fmt.Errorf("invalid draft project directory")
		}
		value, err := lookup(ctx, entry.Name())
		if err != nil && !errors.Is(err, project.ErrNotFound) {
			return err
		}
		versions, err := os.ReadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return err
		}
		for _, version := range versions {
			if !version.IsDir() || !validVersionID(version.Name()) {
				return fmt.Errorf("invalid draft version directory")
			}
			if value.Draft != nil && value.Draft.ID == version.Name() {
				continue
			}
			if err := d.RemoveDraft(entry.Name(), version.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

// OpenVersion 返回已验证版本目录的句柄；调用方负责关闭。
func (d *Deployer) OpenVersion(slug, slot string) (*os.Root, error) {
	directory, err := d.versionDirectory(slug, slot)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, project.ErrNotFound
	}
	return os.OpenRoot(directory)
}
