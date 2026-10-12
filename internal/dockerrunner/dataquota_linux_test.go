//go:build linux && (amd64 || arm64)

package dockerrunner

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

// TestXFSDataQuota 在专用 XFS 临时目录验证真实硬限制、恢复、碰撞和清理；默认跳过。
func TestXFSDataQuota(t *testing.T) {
	base := os.Getenv("STACKNEST_QUOTA_TEST_ROOT")
	if base == "" {
		t.Skip("requires dedicated XFS path and CAP_SYS_ADMIN")
	}
	if unsafe.Sizeof(projectAttr{}) != 28 || unsafe.Sizeof(diskQuota{}) != 112 {
		t.Fatal("Linux quota ABI layout mismatch")
	}
	root, err := os.MkdirTemp(base, "runner-quota-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	id1, id2 := "0c2d84bb-15a1-492b-938a-83282600c112", "45c781e3-1f78-4f37-8f7d-0f6c38341eef"
	dir1, dir2 := filepath.Join(root, "apps", id1, "data"), filepath.Join(root, "apps", id2, "data")
	for _, dir := range []string{dir1, dir2} {
		if err = os.MkdirAll(dir, 0777); err != nil {
			t.Fatal(err)
		}
	}
	seed := filepath.Join(dir1, "existing")
	if err = os.WriteFile(seed, make([]byte, 8<<20), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err = os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(dir1, "link")); err != nil {
		t.Fatal(err)
	}
	manager, err := newDataQuota(root, base, "quota-test", 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	q := manager.(*xfsDataQuota)
	if err = q.Ensure(id1, dir1); err != nil {
		t.Fatal(err)
	}
	if err = q.Ensure(id2, dir2); err != nil {
		t.Fatal(err)
	}
	area, err := q.Usage(id1, dir1)
	if err != nil || area.Source != "xfs" || area.UsedBytes == nil || *area.UsedBytes < 8<<20 || area.LimitBytes == nil || *area.LimitBytes != 32<<20 || area.LimitInodes == nil || *area.LimitInodes != 100000 {
		t.Fatal("invalid actual quota usage", area, err)
	}
	if _, err := q.Usage(id1, dir2); err == nil {
		t.Fatal("cross-application quota read accepted")
	}
	if err := q.Check(); err != nil {
		t.Fatal("quota capability check", err)
	}
	record1, err := readQuotaRecord(filepath.Join(root, "apps", id1, "disk-quota.json"))
	if err != nil || !record1.Ready {
		t.Fatal(record1, err)
	}
	record2, err := readQuotaRecord(filepath.Join(root, "apps", id2, "disk-quota.json"))
	if err != nil || record1.ID == record2.ID {
		t.Fatal("apps share project ID", err)
	}
	fd, err := directoryFD(root)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	value, err := quotaValue(fd, record1.ID)
	if err != nil || value.Hard != 32<<11 || value.Blocks < 8<<11 || value.IHard != 100000 {
		t.Fatal("existing data not accounted", value, err)
	}
	f, err := os.Open(outside)
	if err != nil {
		t.Fatal(err)
	}
	attr := projectAttr{}
	err = projectIOCTL(int(f.Fd()), getProjectAttr, &attr)
	f.Close()
	if err != nil || attr.ID != 0 {
		t.Fatal("symlink target was modified", attr, err)
	}

	full, err := os.Create(filepath.Join(dir1, "fill"))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for total < 40<<20 {
		n, writeErr := full.Write(make([]byte, 1<<20))
		total += n
		if writeErr != nil {
			err = writeErr
			break
		}
	}
	full.Close()
	if !errors.Is(err, syscall.ENOSPC) && !errors.Is(err, syscall.EDQUOT) {
		t.Fatal("hard limit not enforced", total, err)
	}
	if err = os.WriteFile(filepath.Join(dir2, "unaffected"), make([]byte, 1<<20), 0600); err != nil {
		t.Fatal("other app blocked", err)
	}
	if err = os.Remove(filepath.Join(dir1, "fill")); err != nil {
		t.Fatal(err)
	}
	if err = q.Ensure(id1, dir1); err != nil {
		t.Fatal("repeat ensure", err)
	}
	repeated, _ := readQuotaRecord(filepath.Join(root, "apps", id1, "disk-quota.json"))
	if repeated.ID != record1.ID {
		t.Fatal("repeat changed project ID")
	}

	// 相同哈希候选被另一个实例占用时必须换编号；恢复出的旧元数据不能覆盖旧限额。
	clone := filepath.Join(root, "restored")
	restoreDir := filepath.Join(clone, "apps", id1, "data")
	if err = os.MkdirAll(restoreDir, 0777); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(restoreDir, "restored"), make([]byte, 3<<20), 0600); err != nil {
		t.Fatal(err)
	}
	if err = writeQuotaRecord(filepath.Join(clone, "apps", id1, "disk-quota.json"), record1); err != nil {
		t.Fatal(err)
	}
	restored, err := newDataQuota(clone, base, "quota-test", 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.Ensure(id1, restoreDir); err != nil {
		t.Fatal(err)
	}
	record3, _ := readQuotaRecord(filepath.Join(clone, "apps", id1, "disk-quota.json"))
	if record3.ID == record1.ID || record3.ID == record2.ID {
		t.Fatal("restored record reused occupied quota")
	}
	if err = restored.Remove(id1, filepath.Dir(restoreDir)); err != nil {
		t.Fatal("restore removal", err)
	}
	for _, id := range []string{id1, id2} {
		if err = q.Remove(id, filepath.Join(root, "apps", id)); err != nil {
			t.Fatal("quota cleanup", err)
		}
		if err = q.Remove(id, filepath.Join(root, "apps", id)); err != nil {
			t.Fatal("repeat removal", err)
		}
	}
	value, err = quotaValue(fd, record1.ID)
	if err != nil || !unusedProject(value) {
		t.Fatal("quota not released", value, err)
	}
	t.Logf("hard quota enforced after %d additional bytes; independent apps, restore and cleanup passed", total)
}

// TestQuotaChangingIoctlBlocked 验证真实应用 seccomp 阻止 root 修改配额归属和继承标记。
func TestQuotaChangingIoctlBlocked(t *testing.T) {
	path := os.Getenv("STACKNEST_QUOTA_ESCAPE_TEST")
	if path == "" {
		t.Skip("requires application seccomp profile")
	}
	fd, err := directoryFD(path)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	attr := projectAttr{}
	if err = projectIOCTL(fd, getProjectAttr, &attr); err != nil {
		t.Fatal("read attributes", err)
	}
	attr.ID = 0
	attr.Flags &^= projectInherit
	for _, high := range []uintptr{0, 1 << 32, 0xffffffff00000000} {
		if err = projectIOCTL(fd, setProjectAttr|high, &attr); !errors.Is(err, syscall.EACCES) {
			t.Fatal("project ID change was not blocked", high, err)
		}
	}
	for _, cmd := range []uintptr{0x40086602, 0x40046602, 0x140086602, 0xffffffff40046602} {
		var flags uint64
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), cmd, uintptr(unsafe.Pointer(&flags)))
		if errno != syscall.EACCES {
			t.Fatal("chattr was not blocked", cmd, errno)
		}
	}
	t.Log("root quota escape via ioctl and chattr rejected")
}

// TestContainerLayerHardLimit 使用真实可写层实际写入，确认超限而不是仅检查配置。
func TestContainerLayerHardLimit(t *testing.T) {
	if os.Getenv("STACKNEST_LAYER_QUOTA_TEST") != "1" {
		t.Skip("requires 64 MiB container layer limit")
	}
	f, err := os.Create("/quota-layer-fill")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	total := 0
	block := make([]byte, 1<<20)
	for total < 72<<20 {
		n, writeErr := f.Write(block)
		total += n
		if writeErr != nil {
			err = writeErr
			break
		}
	}
	if !errors.Is(err, syscall.ENOSPC) && !errors.Is(err, syscall.EDQUOT) {
		t.Fatal("writable layer quota did not reject excess write", total, err)
	}
	if total > 64<<20 {
		t.Fatal("write exceeded configured size", total)
	}
	t.Logf("64 MiB writable-layer quota rejected writing after %d bytes", total)
}
