package dockerrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"time"
)

const egressBridgePrefix = "sne-"

type egressStatus struct {
	Version      int       `json:"version"`
	Ready        bool      `json:"ready"`
	CheckedAt    time.Time `json:"checkedAt"`
	BridgePrefix string    `json:"bridgePrefix"`
	BlockedCIDRs []string  `json:"blockedCIDRs"`
	DNSServers   []string  `json:"dnsServers"`
}

// readEgressStatus 只接受宿主只读目录内的新鲜策略状态，缺失或失效时不开放联网。
func readEgressStatus(path string) (egressStatus, error) {
	var value egressStatus
	file, err := os.Open(path)
	if err != nil {
		return value, errors.New("应用联网策略不可用，请检查宿主 stacknest-egress 服务")
	}
	defer file.Close()
	if err = json.NewDecoder(io.LimitReader(file, 32<<10)).Decode(&value); err != nil {
		return value, errors.New("应用联网策略状态无效")
	}
	age := time.Since(value.CheckedAt)
	if value.Version != 1 || !value.Ready || value.BridgePrefix != egressBridgePrefix ||
		age < -5*time.Second || age > 30*time.Second || len(value.BlockedCIDRs) == 0 || len(value.BlockedCIDRs) > 256 ||
		len(value.DNSServers) == 0 || len(value.DNSServers) > 3 {
		return value, errors.New("应用联网策略未就绪，请检查宿主 stacknest-egress 服务")
	}
	for _, cidr := range value.BlockedCIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || prefix.Bits() < 8 || (prefix.Addr().Is6() && prefix.Bits() < 32) {
			return value, errors.New("应用联网策略网段无效")
		}
	}
	for _, server := range value.DNSServers {
		ip, err := netip.ParseAddr(server)
		if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
			ip.Is4In6() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
			return value, errors.New("应用联网策略 DNS 无效")
		}
	}
	return value, nil
}

// checkEgress 不影响离线模式的停止、删除和代理，只限制开启联网的启动操作。
func (r *Runner) checkEgress() error {
	if r.egressFile == "" {
		return nil
	}
	_, err := readEgressStatus(r.egressFile)
	return err
}

// configureEgress 校验宿主只读挂载和 API 版本，不给运行器增加宿主网络权限。
func (r *Runner) configureEgress(ctx context.Context, self inspection) error {
	r.egressFile = os.Getenv("STACKNEST_APP_EGRESS_POLICY")
	if r.egressFile == "" {
		return nil
	}
	if r.egressFile != "/run/stacknest-egress/status.json" {
		return errors.New("应用联网策略必须使用固定的宿主状态路径")
	}
	found := false
	for _, mount := range self.Mounts {
		if mount.Type == "bind" && mount.Destination == filepath.Dir(r.egressFile) && !mount.RW {
			found = true
		}
	}
	if !found {
		return errors.New("应用联网策略必须由宿主只读挂载")
	}
	if err := r.checkEgress(); err != nil {
		return err
	}
	var version struct{ APIVersion string }
	if err := r.engine.call(ctx, "GET", "/version", nil, &version); err != nil {
		return err
	}
	var major, minor int
	if _, err := fmt.Sscanf(version.APIVersion, "%d.%d", &major, &minor); err != nil || major != 1 || minor < 48 {
		return errors.New("开放应用联网需要 Docker API 1.48 或更新版本")
	}
	r.engine.version = "1.48"
	return nil
}

// bridgeName 生成符合 Linux 名称长度限制且包含实例身份的应用网桥名。
func (r *Runner) bridgeName(id string) string {
	digest := sha256.Sum256([]byte(r.instance + ":" + id))
	return egressBridgePrefix + hex.EncodeToString(digest[:5])
}
