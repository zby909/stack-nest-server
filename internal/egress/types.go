package egress

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"
)

const Table = "stacknest_egress"
const BridgePrefix = "sne-"

var ErrUnavailable = errors.New("宿主网络服务未就绪")
var ErrConflict = errors.New("配置已被更新，请刷新后重试")
var interfaceName = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,15}$`)

// Exception 仅允许某个 IP 的指定协议和端口。
type Exception struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// Config 是超管可管理的结构化网络策略。
type Config struct {
	LANInterfaces []string    `json:"lanInterfaces"`
	BlockedCIDRs  []string    `json:"blockedCIDRs"`
	Allow         []Exception `json:"allow"`
	DNSServers    []string    `json:"dnsServers"`
}

// Status 保持运行器只读状态的兼容格式。
type Status struct {
	Version      int       `json:"version"`
	Ready        bool      `json:"ready"`
	CheckedAt    time.Time `json:"checkedAt"`
	BridgePrefix string    `json:"bridgePrefix"`
	BlockedCIDRs []string  `json:"blockedCIDRs"`
	DNSServers   []string  `json:"dnsServers"`
}

// Interface 描述可配置网卡及其实际地址，不扫描局域网设备。
type Interface struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	Eligible  bool     `json:"eligible"`
}

// View 返回实际配置、当前有效范围及乐观锁版本。
type View struct {
	Config                Config      `json:"config"`
	Revision              string      `json:"revision"`
	Ready                 bool        `json:"ready"`
	CheckedAt             time.Time   `json:"checkedAt"`
	EffectiveBlockedCIDRs []string    `json:"effectiveBlockedCIDRs"`
	Interfaces            []Interface `json:"interfaces"`
	Error                 string      `json:"error,omitempty"`
	DNSChanged            bool        `json:"dnsChanged,omitempty"`
}

// Update 要求提交页面读取到的配置版本，避免覆盖其他人的修改。
type Update struct {
	Config           Config `json:"config"`
	ExpectedRevision string `json:"expectedRevision"`
}

// Normalize 校验所有配置字段，拒绝命令注入和无意封锁全网。
func Normalize(c Config) (Config, error) {
	if len(c.LANInterfaces) < 1 || len(c.LANInterfaces) > 32 {
		return c, errors.New("请选择 1 至 32 个局域网接口")
	}
	for _, name := range c.LANInterfaces {
		if !interfaceName.MatchString(name) {
			return c, errors.New("网卡名称无效")
		}
	}
	c.LANInterfaces = unique(c.LANInterfaces)
	if len(c.BlockedCIDRs) > 128 || len(c.Allow) > 128 {
		return c, errors.New("网段和允许例外分别最多 128 项")
	}
	for _, raw := range c.BlockedCIDRs {
		p, err := netip.ParsePrefix(raw)
		if err != nil || p.Addr().Zone() != "" || p != p.Masked() || p.Addr().Is4In6() || p.Bits() < 8 || (p.Addr().Is6() && p.Bits() < 32) {
			return c, errors.New("请输入完整网段地址；IPv4 前缀至少为 /8，IPv6 至少为 /32")
		}
	}
	c.BlockedCIDRs = unique(c.BlockedCIDRs)
	if c.Allow == nil {
		c.Allow = []Exception{}
	}
	for i, e := range c.Allow {
		ip, err := netip.ParseAddr(e.Address)
		if err != nil || ip.Zone() != "" || ip.Is4In6() || ip.IsUnspecified() || ip.IsMulticast() || e.Port < 1 || e.Port > 65535 || (e.Protocol != "tcp" && e.Protocol != "udp") {
			return c, errors.New("允许例外必须是有效 IP、TCP/UDP 和 1 至 65535 的端口")
		}
		c.Allow[i].Address = ip.String()
	}
	if len(c.DNSServers) < 1 || len(c.DNSServers) > 3 {
		return c, errors.New("请设置 1 至 3 个公网 DNS 地址")
	}
	for i, raw := range c.DNSServers {
		ip, err := netip.ParseAddr(raw)
		if err != nil || !publicDNS(ip) {
			return c, errors.New("DNS 必须使用公网单播地址")
		}
		c.DNSServers[i] = ip.String()
	}
	c.DNSServers = orderedUnique(c.DNSServers)
	return c, nil
}

// publicDNS 排除私有、文档、保留和带区域标识的地址。
func publicDNS(ip netip.Addr) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Is4In6() || ip.Zone() != "" {
		return false
	}
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "3fff::/20"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return true
}

// unique 保留非空 JSON 数组并统一顺序。
func unique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, v := range values {
		if !seen[v] {
			result = append(result, v)
			seen[v] = true
		}
	}
	sort.Strings(result)
	return result
}

// orderedUnique 保留 DNS 的首选顺序，避免排序改变故障回退行为。
func orderedUnique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result
}

// Ranges 合并指定接口实际网段和 NAT 外的额外网段。
func Ranges(c Config, interfaces []Interface) ([]string, error) {
	ranges := append([]string{}, c.BlockedCIDRs...)
	for _, name := range c.LANInterfaces {
		found := false
		for _, iface := range interfaces {
			if iface.Name != name {
				continue
			}
			found = true
			for _, raw := range iface.Addresses {
				p, err := netip.ParsePrefix(raw)
				if err != nil || p.Bits() < 8 || (p.Addr().Is6() && p.Bits() < 32) {
					return nil, errors.New("网卡地址范围过大或无效")
				}
				ranges = append(ranges, p.Masked().String())
			}
		}
		if !found {
			return nil, fmt.Errorf("网卡 %s 不存在", name)
		}
	}
	if len(ranges) == 0 {
		return nil, errors.New("选定网卡暂无地址")
	}
	return unique(ranges), nil
}

// Render 生成独立规则表，先识别宿主地址和单端口例外，再拒绝局域网流量。
func Render(c Config, ranges []string, exists bool) string {
	lines := []string{}
	if exists {
		lines = append(lines, "delete table inet "+Table)
	}
	lines = append(lines, "add table inet "+Table)
	for _, family := range []struct {
		name, kind string
		v4         bool
	}{{"lan4", "ipv4_addr", true}, {"lan6", "ipv6_addr", false}} {
		lines = append(lines, fmt.Sprintf("add set inet %s %s { type %s; flags interval; auto-merge; }", Table, family.name, family.kind))
		members := []string{}
		for _, raw := range ranges {
			if netip.MustParsePrefix(raw).Addr().Is4() == family.v4 {
				members = append(members, raw)
			}
		}
		if len(members) > 0 {
			lines = append(lines, fmt.Sprintf("add element inet %s %s { %s }", Table, family.name, strings.Join(members, ", ")))
		}
	}
	lines = append(lines,
		"add chain inet "+Table+" ingress { type filter hook prerouting priority -110; policy accept; }",
		"add chain inet "+Table+" outbound",
		"add rule inet "+Table+" ingress iifname \""+BridgePrefix+"*\" jump outbound",
		"add rule inet "+Table+" outbound ct direction reply ct state established,related return",
		"add rule inet "+Table+" outbound fib daddr type local return")
	for _, e := range c.Allow {
		family := "ip"
		if netip.MustParseAddr(e.Address).Is6() {
			family = "ip6"
		}
		lines = append(lines, fmt.Sprintf("add rule inet %s outbound %s daddr %s %s dport %d return", Table, family, e.Address, e.Protocol, e.Port))
	}
	lines = append(lines, "add rule inet "+Table+" outbound ip daddr @lan4 counter drop", "add rule inet "+Table+" outbound ip6 daddr @lan6 counter drop")
	return strings.Join(lines, "\n") + "\n"
}

// hashBytes 生成配置版本和规则指纹，不包含明文敏感内容。
func hashBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// fingerprint 忽略规则句柄和实时计数，避免每次流量变化都重建表。
func fingerprint(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range v {
			if key != "metainfo" && key != "handle" && key != "packets" && key != "bytes" {
				out[key] = fingerprint(item)
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if _, skip := m["metainfo"]; skip {
					continue
				}
			}
			out = append(out, fingerprint(item))
		}
		return out
	default:
		return value
	}
}

// ruleHash 将规则结构归一化为可比较的指纹。
func ruleHash(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	b, err := json.Marshal(fingerprint(v))
	return hashBytes(b), err
}
