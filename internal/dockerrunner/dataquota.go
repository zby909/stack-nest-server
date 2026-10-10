package dockerrunner

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"stacknest/server/internal/containerapp"
)

// dataQuota 只操作平台生成的数据目录，失败时禁止启动不限额的应用。
type dataQuota interface {
	Ensure(id, dir string) error
	Remove(id, dir string) error
}

// Moby 官方默认 seccomp（2026-10-10），仅增加禁止修改项目配额属性的 ioctl 规则。
// 上游 SHA256: 6416b47770785a41ac59073cdc77d9fe98517df2799dc83ef207e622de3053f6。
// https://github.com/moby/profiles/blob/main/seccomp/default.json；许可证见 seccomp-LICENSE。
//go:embed seccomp-quota.json
var quotaSeccomp string

// configureDataQuota 显式开启时要求文件系统、内核和权限均支持硬配额。
func (r *Runner) configureDataQuota(dockerStorage string) error {
	raw := os.Getenv("STACKNEST_APP_DATA_LIMIT_MIB")
	if raw == "" || raw == "0" {
		return nil
	}
	limit, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || limit < 16 || limit > 32768 {
		return errors.New("STACKNEST_APP_DATA_LIMIT_MIB 应为 16 至 32768，或 0（禁用）")
	}
	r.quota, err = newDataQuota(r.dataDir, dockerStorage, r.instance, limit<<20)
	if err != nil {
		return fmt.Errorf("应用数据硬配额不可用: %w", err)
	}
	entries, err := os.ReadDir(filepath.Join(r.dataDir, "apps"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !containerapp.ValidID(entry.Name()) || !entry.IsDir() {
			continue
		}
		dir := filepath.Join(r.dataDir, "apps", entry.Name(), "data")
		if _, err = os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err = r.quota.Ensure(entry.Name(), dir); err != nil {
			return fmt.Errorf("初始化应用 %s 的数据配额: %w", entry.Name(), err)
		}
	}
	return nil
}

// protectedQuotaPolicy 只接受本运行器生成的完整策略，避免通配允许等规则覆盖属性禁令。
func protectedQuotaPolicy(options []string) bool {
	var expected any
	if json.Unmarshal([]byte(quotaSeccomp), &expected) != nil {
		return false
	}
	want, _ := json.Marshal(expected)
	for _, option := range options {
		if !strings.HasPrefix(option, "seccomp=") {
			continue
		}
		var actual any
		if json.Unmarshal([]byte(strings.TrimPrefix(option, "seccomp=")), &actual) != nil {
			return false
		}
		got, _ := json.Marshal(actual)
		return string(got) == string(want)
	}
	return false
}
