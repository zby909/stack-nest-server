//go:build linux && (amd64 || arm64)

package dockerrunner

import (
	"errors"
	"os"
	"path/filepath"
	"stacknest/server/internal/containerapp"
	"strings"
	"syscall"
)

// projectUsage 从内核读取已分配块和 inode，上限为零表示未设硬限制。
func projectUsage(fd int, id uint32) (containerapp.DiskArea, error) {
	if id == 0 {
		return containerapp.DiskArea{}, errors.New("missing project quota")
	}
	if err := quotaEnabled(fd); err != nil {
		return containerapp.DiskArea{}, err
	}
	value, err := quotaValue(fd, id)
	if err != nil || value.ID != id || value.Hard == 0 {
		return containerapp.DiskArea{}, errors.New("quota unavailable")
	}
	used, limit := value.Blocks*512, value.Hard*512
	area := containerapp.DiskArea{UsedBytes: &used, LimitBytes: &limit, UsedInodes: &value.Inodes, Source: "xfs"}
	if value.IHard > 0 {
		area.LimitInodes = &value.IHard
	}
	return area, nil
}

// Check 只验证当前文件系统配额状态，不分配编号或修改磁盘。
func (q *xfsDataQuota) Check() error {
	for _, path := range []string{q.root, q.dockerStorage} {
		fd, err := directoryFD(path)
		if err != nil {
			return err
		}
		err = quotaEnabled(fd)
		syscall.Close(fd)
		if err != nil {
			return err
		}
	}
	return nil
}

// Usage 读取持久化目录前核对平台元数据、目录属性和所属文件系统。
func (q *xfsDataQuota) Usage(id, dir string) (containerapp.DiskArea, error) {
	path, err := q.recordPath(id, dir)
	if err != nil {
		return containerapp.DiskArea{}, err
	}
	record, err := readQuotaRecord(path)
	if err != nil || !record.Ready || record.ID == 0 {
		return containerapp.DiskArea{}, errors.New("quota not ready")
	}
	fd, err := directoryFD(dir)
	if err != nil {
		return containerapp.DiskArea{}, err
	}
	defer syscall.Close(fd)
	attr := projectAttr{}
	if err = projectIOCTL(fd, getProjectAttr, &attr); err != nil || attr.ID != record.ID {
		return containerapp.DiskArea{}, errors.New("quota ownership mismatch")
	}
	return projectUsage(fd, record.ID)
}

// WritableUsage 仅解析已验证托管容器的 Docker UpperDir，使用目录能力限制在 Docker 存储内。
func (q *xfsDataQuota) WritableUsage(upper, dockerRoot string) (containerapp.DiskArea, error) {
	if !filepath.IsAbs(upper) || !filepath.IsAbs(dockerRoot) {
		return containerapp.DiskArea{}, errors.New("missing overlay path")
	}
	rel, err := filepath.Rel(dockerRoot, upper)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return containerapp.DiskArea{}, errors.New("invalid overlay path")
	}
	root, err := os.OpenRoot(q.dockerStorage)
	if err != nil {
		return containerapp.DiskArea{}, err
	}
	defer root.Close()
	dir, err := root.Open(rel)
	if err != nil {
		return containerapp.DiskArea{}, err
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		return containerapp.DiskArea{}, errors.New("invalid overlay directory")
	}
	attr := projectAttr{}
	fd := int(dir.Fd())
	if err = projectIOCTL(fd, getProjectAttr, &attr); err != nil || attr.ID == 0 || attr.ID >= projectBase {
		return containerapp.DiskArea{}, errors.New("invalid overlay quota")
	}
	return projectUsage(fd, attr.ID)
}
