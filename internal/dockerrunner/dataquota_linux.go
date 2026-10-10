//go:build linux && (amd64 || arm64)

package dockerrunner

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"stacknest/server/internal/containerapp"
)

const projectBase = uint32(1 << 30) // 避开 Docker overlay2 从低位递增的配额编号。
const projectInherit = uint32(0x200)
const getProjectAttr = uintptr(0x801c581f)
const setProjectAttr = uintptr(0x401c5820)

// 下列结构与 Linux UAPI fsxattr、fs_disk_quota 的布局一致；块单位为 512 字节。
type projectAttr struct {
	Flags, ExtSize, Nextents, ID, CowExtSize uint32
	Pad                                      [8]byte
}

type diskQuota struct {
	Version, Flags           int8
	Mask                     uint16
	ID                       uint32
	Hard, Soft, IHard, ISoft uint64
	Blocks, Inodes           uint64
	ITimer, BTimer           int32
	IWarns, BWarns           uint16
	TimerHi                  [4]byte
	RTHard, RTSoft, RTBlocks uint64
	RTTimer                  int32
	RTWarns                  uint16
	Pad3                     int16
	Pad4                     [8]byte
}

type quotaRecord struct {
	ID    uint32 `json:"id"`
	Ready bool   `json:"ready"`
}

type xfsDataQuota struct {
	root, dockerStorage, instance string
	limit                         uint64
}

// quotaCall 使用挂载内目录的文件描述符，运行器不需要访问宿主块设备。
func quotaCall(fd int, command uintptr, id uint32, value unsafe.Pointer) error {
	_, _, err := syscall.Syscall6(443, uintptr(fd), command, uintptr(id), uintptr(value), 0, 0) // quotactl_fd
	runtime.KeepAlive(value)
	if err != 0 {
		return err
	}
	return nil
}

// projectIOCTL 读取或设置项目属性，保留与配额无关的文件属性。
func projectIOCTL(fd int, command uintptr, value *projectAttr) error {
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), command, uintptr(unsafe.Pointer(value)))
	runtime.KeepAlive(value)
	if err != 0 {
		return err
	}
	return nil
}

// directoryFD 不跟随目录本身的符号链接。
func directoryFD(path string) (int, error) {
	return syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
}

// quotaEnabled 同时验证 XFS 和项目配额的记账、执行状态。
func quotaEnabled(fd int) error {
	var fs syscall.Statfs_t
	if err := syscall.Fstatfs(fd, &fs); err != nil {
		return err
	}
	if fs.Type != 0x58465342 {
		return errors.New("数据卷必须位于 XFS 文件系统")
	}
	var state [160]byte // fs_quota_statv version 1，64 位布局。
	state[0] = 1
	if err := quotaCall(fd, 0x580802, 0, unsafe.Pointer(&state[0])); err != nil {
		return err
	}
	if binary.LittleEndian.Uint16(state[2:4])&0x30 != 0x30 {
		return errors.New("XFS 项目配额记账和执行必须开启")
	}
	return nil
}

// newDataQuota 验证数据和 Docker 存储属于同一 XFS，跨实例共用目录锁分配编号。
func newDataQuota(root, dockerStorage, instance string, limit int64) (dataQuota, error) {
	fd, err := directoryFD(root)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	if err = quotaEnabled(fd); err != nil {
		return nil, err
	}
	lock, err := directoryFD(dockerStorage)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(lock)
	var data, docker syscall.Stat_t
	if err = syscall.Fstat(fd, &data); err == nil {
		err = syscall.Fstat(lock, &docker)
	}
	if err != nil || data.Dev != docker.Dev {
		return nil, errors.New("应用数据与 Docker 存储必须位于同一 XFS 挂载")
	}
	return &xfsDataQuota{root: root, dockerStorage: dockerStorage, instance: instance, limit: uint64(limit) / 512}, nil
}

// locked 在所有共享该文件系统的运行器间串行设置或释放配额。
func (q *xfsDataQuota) locked(fn func(fd int) error) error {
	lock, err := directoryFD(q.dockerStorage)
	if err != nil {
		return err
	}
	defer syscall.Close(lock)
	if err = syscall.Flock(lock, syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(lock, syscall.LOCK_UN)
	fd, err := directoryFD(q.root)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	if err = quotaEnabled(fd); err != nil {
		return err
	}
	return fn(fd)
}

// quotaValue 仅 ENOENT 表示编号尚无使用，其他错误绝不能当成空闲。
func quotaValue(fd int, id uint32) (diskQuota, error) {
	value := diskQuota{Version: 1, Flags: 2, ID: id}
	err := quotaCall(fd, 0x580302, id, unsafe.Pointer(&value))
	if errors.Is(err, syscall.ENOENT) {
		return diskQuota{Version: 1, Flags: 2, ID: id}, nil
	}
	return value, err
}

// unusedProject 只复用既无数据也无额度的编号，不能撞上其他应用或 Docker。
func unusedProject(value diskQuota) bool {
	return value.Blocks == 0 && value.Inodes == 0 && value.Hard == 0 && value.Soft == 0 && value.IHard == 0 && value.ISoft == 0 && value.RTHard == 0 && value.RTSoft == 0 && value.RTBlocks == 0
}

// allocate 根据应用身份选择高位编号，再检查文件系统防止哈希碰撞。
func (q *xfsDataQuota) allocate(fd int, id string) (uint32, error) {
	digest := sha256.Sum256([]byte(q.instance + "/" + id))
	start := binary.BigEndian.Uint32(digest[:4]) & (projectBase - 1)
	for offset := uint32(0); offset < 1024; offset++ {
		candidate := projectBase | ((start + offset) & (projectBase - 1))
		value, err := quotaValue(fd, candidate)
		if err != nil {
			return 0, err
		}
		if unusedProject(value) {
			return candidate, nil
		}
	}
	return 0, errors.New("没有可用的应用配额编号")
}

// recordPath 校验应用根目录，元数据放在用户不可挂载的 data 目录之外。
func (q *xfsDataQuota) recordPath(id, dir string) (string, error) {
	parent := filepath.Join(q.root, "apps", id)
	if !containerapp.ValidID(id) || dir != filepath.Join(parent, "data") {
		return "", errors.New("应用配额路径无效")
	}
	for _, path := range []string{filepath.Join(q.root, "apps"), parent} {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", errors.New("应用配额目录状态异常")
		}
	}
	return filepath.Join(parent, "disk-quota.json"), nil
}

// readQuotaRecord 读取受保护的配额归属，拒绝符号链接和异常编号。
func readQuotaRecord(path string) (quotaRecord, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ENOENT) {
		return quotaRecord{}, nil
	}
	if err != nil {
		return quotaRecord{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256 {
		return quotaRecord{}, errors.New("应用配额元数据异常")
	}
	var record quotaRecord
	if err = json.NewDecoder(f).Decode(&record); err != nil || record.ID < projectBase || record.ID >= projectBase*2 {
		return quotaRecord{}, errors.New("应用配额元数据无效")
	}
	return record, nil
}

// writeQuotaRecord 原子保存归属，初始化中断后可继续相同编号而非放宽限制。
func writeQuotaRecord(path string, record quotaRecord) error {
	data, _ := json.Marshal(record)
	f, err := os.CreateTemp(filepath.Dir(path), ".disk-quota-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	return err
}

// assignTree 从目录描述符递归分配，拒绝跨文件系统，符号链接不跟随。
func assignTree(fd int, id uint32, device uint64) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return err
	}
	if uint64(stat.Dev) != device {
		return errors.New("应用数据不能跨越文件系统")
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR && stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return errors.New("应用数据含不支持的特殊文件")
	}
	attr := projectAttr{}
	if err := projectIOCTL(fd, getProjectAttr, &attr); err != nil {
		return err
	}
	attr.ID = id
	if stat.Mode&syscall.S_IFMT == syscall.S_IFDIR {
		attr.Flags |= projectInherit
	}
	if err := projectIOCTL(fd, setProjectAttr, &attr); err != nil {
		return err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return nil
	}
	dup, err := syscall.Dup(fd)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(dup), "application-data")
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return errors.New("应用数据含不支持的特殊文件")
		}
		child, err := syscall.Openat(fd, entry.Name(), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		err = assignTree(child, id, device)
		syscall.Close(child)
		if err != nil {
			return err
		}
	}
	return nil
}

// Ensure 在启动前设置 512 字节块上限和 inode 上限，恢复后重新应用文件属性。
func (q *xfsDataQuota) Ensure(id, dir string) error {
	path, err := q.recordPath(id, dir)
	if err != nil {
		return err
	}
	return q.locked(func(fd int) error {
		root, err := directoryFD(dir)
		if err != nil {
			return err
		}
		defer syscall.Close(root)
		attr := projectAttr{}
		if err = projectIOCTL(root, getProjectAttr, &attr); err != nil {
			return err
		}
		record, err := readQuotaRecord(path)
		if err != nil {
			return err
		}
		if attr.ID != 0 && (record.ID == 0 || attr.ID != record.ID) {
			return errors.New("应用数据配额归属异常")
		}
		if attr.ID == 0 {
			// tar 恢复不带文件系统项目属性，必须在目标机重新分配。
			record.ID, err = q.allocate(fd, id)
			record.Ready = false
			if err != nil {
				return err
			}
			if err = writeQuotaRecord(path, record); err != nil {
				return err
			}
		}
		limit := diskQuota{Version: 1, Flags: 2, Mask: 15, ID: record.ID, Hard: q.limit, IHard: 100000}
		if err = quotaCall(fd, 0x580402, record.ID, unsafe.Pointer(&limit)); err != nil {
			return fmt.Errorf("设置配额失败（需要 CAP_SYS_ADMIN）: %w", err)
		}
		if !record.Ready || attr.Flags&projectInherit == 0 {
			var stat syscall.Stat_t
			if err = syscall.Fstat(fd, &stat); err == nil {
				err = assignTree(root, record.ID, uint64(stat.Dev))
			}
			if err != nil {
				return err
			}
			record.Ready = true
			return writeQuotaRecord(path, record)
		}
		return nil
	})
}

// Remove 先删除本应用的数据，再释放已无文件的编号，失败保留元数据供重试。
func (q *xfsDataQuota) Remove(id, dir string) error {
	path, err := q.recordPath(id, filepath.Join(dir, "data"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return q.locked(func(fd int) error {
		record, err := readQuotaRecord(path)
		if err != nil {
			return err
		}
		root, err := directoryFD(filepath.Join(dir, "data"))
		if err == nil {
			attr := projectAttr{}
			err = projectIOCTL(root, getProjectAttr, &attr)
			syscall.Close(root)
			if err != nil {
				return err
			}
			if attr.ID == 0 {
				record.ID = 0 // 恢复后尚未初始化的元数据不能影响目标文件系统的已有配额。
			} else if attr.ID != record.ID {
				return errors.New("应用数据配额归属异常")
			}
		} else if !errors.Is(err, syscall.ENOENT) {
			return err
		}
		if err = os.RemoveAll(filepath.Join(dir, "data")); err != nil {
			return err
		}
		if record.ID != 0 {
			// XFS 的 inode 回收是异步的；同步文件系统后再确认编号已无占用。
			if err := syncQuotaFS(fd); err != nil {
				return err
			}
			value, err := quotaValue(fd, record.ID)
			if err != nil {
				return err
			}
			if value.Blocks != 0 || value.Inodes != 0 {
				return errors.New("应用配额仍有文件占用，稍后重试删除")
			}
			limit := diskQuota{Version: 1, Flags: 2, Mask: 15, ID: record.ID}
			if err = quotaCall(fd, 0x580402, record.ID, unsafe.Pointer(&limit)); err != nil {
				return err
			}
		}
		return os.RemoveAll(dir)
	})
}

// syncQuotaFS 等待 XFS 延迟回收；旧版 Go syscall 未收录这两个架构的 syncfs 常量。
func syncQuotaFS(fd int) error {
	number := uintptr(306) // Linux x86_64
	if runtime.GOARCH == "arm64" {
		number = 267
	}
	_, _, errno := syscall.Syscall(number, uintptr(fd), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
