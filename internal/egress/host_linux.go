package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Paths 固定生产配置路径，测试可使用独立命名空间内的临时目录。
type Paths struct{ Config, State string }

// DefaultPaths 返回不向普通应用挂载的宿主配置和控制目录。
func DefaultPaths() Paths {
	return Paths{Config: "/etc/stacknest-egress/config.json", State: "/run/stacknest-egress"}
}

// Manager 串行维护规则、持久配置和就绪状态。
type Manager struct {
	mu              sync.Mutex
	paths           Paths
	view            View
	desired, actual string
}

// command 仅执行固定程序和分离的参数，不经过 shell。
func command(ctx context.Context, input string, name string, args ...string) ([]byte, error) {
	task, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(task, name, args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s 执行失败", name)
	}
	return out, nil
}

// atomicJSON 同目录原子替换，读取方不会读到半份配置或状态。
func atomicJSON(path string, value any, mode os.FileMode) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), ".egress-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// decode 严格读取一个有限 JSON 对象，拒绝未知字段和尾随内容。
func decode(reader io.Reader, target any) error {
	d := json.NewDecoder(io.LimitReader(reader, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("配置 JSON 无效或存在未知字段")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("配置包含多余内容")
	}
	return nil
}

// inventory 读取网卡和地址；自动候选排除 Docker、VPN 和桥成员。
func inventory(ctx context.Context) ([]Interface, error) {
	linksRaw, err := command(ctx, "", "ip", "-j", "-d", "link", "show")
	if err != nil {
		return nil, err
	}
	addressRaw, err := command(ctx, "", "ip", "-j", "address", "show")
	if err != nil {
		return nil, err
	}
	var links []struct {
		Name   string `json:"ifname"`
		Type   string `json:"link_type"`
		Master any    `json:"master"`
		Info   struct {
			Kind string `json:"info_kind"`
		} `json:"linkinfo"`
	}
	var addresses []struct {
		Name    string `json:"ifname"`
		Entries []struct {
			Family    string `json:"family"`
			Local     string `json:"local"`
			Prefix    int    `json:"prefixlen"`
			Scope     string `json:"scope"`
			Tentative bool   `json:"tentative"`
			Failed    bool   `json:"dadfailed"`
		} `json:"addr_info"`
	}
	if json.Unmarshal(linksRaw, &links) != nil || json.Unmarshal(addressRaw, &addresses) != nil {
		return nil, errors.New("无法读取网卡结构")
	}
	result := make([]Interface, 0, len(links))
	for _, link := range links {
		iface := Interface{Name: link.Name, Addresses: []string{}}
		global := false
		for _, address := range addresses {
			if address.Name != link.Name {
				continue
			}
			for _, e := range address.Entries {
				if (e.Family != "inet" && e.Family != "inet6") || e.Tentative || e.Failed {
					continue
				}
				ip, err := netip.ParseAddr(e.Local)
				if err != nil {
					return nil, errors.New("网卡地址无效")
				}
				iface.Addresses = append(iface.Addresses, netip.PrefixFrom(ip, e.Prefix).String())
				global = global || e.Scope == "global"
			}
		}
		iface.Eligible = global && link.Type == "ether" && link.Master == nil && (link.Info.Kind == "" || link.Info.Kind == "vlan" || link.Info.Kind == "bond") && link.Name != "docker0"
		iface.Addresses = unique(iface.Addresses)
		result = append(result, iface)
	}
	return result, nil
}

// tableState 只读取自己的表，其他宿主及 Docker 规则保持原样。
func tableState(ctx context.Context) ([]byte, bool, error) {
	raw, err := command(ctx, "", "nft", "-j", "list", "table", "inet", Table)
	if err == nil {
		return raw, true, nil
	}
	all, checkErr := command(ctx, "", "nft", "-j", "list", "tables")
	if checkErr != nil {
		return nil, false, checkErr
	}
	var value struct {
		Tables []struct {
			Table struct {
				Name   string `json:"name"`
				Family string `json:"family"`
			} `json:"table"`
		} `json:"nftables"`
	}
	if json.Unmarshal(all, &value) != nil {
		return nil, false, errors.New("无法读取规则表")
	}
	for _, t := range value.Tables {
		if t.Table.Name == Table && t.Table.Family == "inet" {
			return nil, false, err
		}
	}
	return nil, false, nil
}

// withLock 协调安装、刷新和命令行核验，避免规则事务相互覆盖。
func (m *Manager) withLock(action func() error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.paths.State, 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(m.paths.State, "policy.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return action()
}

// loadConfig 校验管理员文件，保留文件摘要用于并发修改保护。
func (m *Manager) loadConfig() (Config, string, error) {
	raw, err := os.ReadFile(m.paths.Config)
	if err != nil {
		return Config{}, "", err
	}
	var c Config
	if err = decode(bytes.NewReader(raw), &c); err != nil {
		return c, hashBytes(raw), err
	}
	c, err = Normalize(c)
	return c, hashBytes(raw), err
}

// apply 在一个 nft 事务内替换本表，失败时旧规则继续生效。
func (m *Manager) apply(ctx context.Context, c Config, ranges []string, exists bool) error {
	text := Render(c, ranges, exists)
	if _, err := command(ctx, text, "nft", "--check", "-f", "-"); err != nil {
		return err
	}
	_, err := command(ctx, text, "nft", "-f", "-")
	return err
}

// synchronize 核验实际规则并刷新状态，不因计数变化重建规则。
func (m *Manager) synchronize(ctx context.Context) error {
	return m.withLock(func() error {
		c, revision, err := m.loadConfig()
		m.view.Revision = revision
		if err != nil {
			return m.failed(err)
		}
		interfaces, err := inventory(ctx)
		if err != nil {
			return m.failed(err)
		}
		ranges, err := Ranges(c, interfaces)
		if err != nil {
			return m.failed(err)
		}
		wantedRaw, _ := json.Marshal([]any{c, ranges})
		wanted := hashBytes(wantedRaw)
		table, exists, err := tableState(ctx)
		if err != nil {
			return m.failed(err)
		}
		actual := ""
		if exists {
			actual, err = ruleHash(table)
			if err != nil {
				return m.failed(err)
			}
		}
		if wanted != m.desired || actual != m.actual {
			if err = m.apply(ctx, c, ranges, exists); err != nil {
				return m.failed(err)
			}
			table, exists, err = tableState(ctx)
			if err != nil || !exists {
				return m.failed(ErrUnavailable)
			}
			actual, err = ruleHash(table)
			if err != nil {
				return m.failed(err)
			}
		}
		m.desired, m.actual = wanted, actual
		m.view = View{Config: c, Revision: revision, Ready: true, CheckedAt: time.Now().UTC(), EffectiveBlockedCIDRs: ranges, Interfaces: interfaces}
		return m.publish()
	})
}

// publish 写入兼容运行器的只读状态。
func (m *Manager) publish() error {
	s := Status{Version: 1, Ready: m.view.Ready, CheckedAt: m.view.CheckedAt, BridgePrefix: BridgePrefix, BlockedCIDRs: m.view.EffectiveBlockedCIDRs, DNSServers: m.view.Config.DNSServers}
	return atomicJSON(filepath.Join(m.paths.State, "status.json"), s, 0644)
}

// failed 保留旧规则并阻止新的联网操作，同时允许超管读取故障。
func (m *Manager) failed(err error) error {
	m.desired = ""
	m.view.Ready = false
	m.view.Error = "网络策略未就绪：" + err.Error()
	if e := m.publish(); e != nil {
		return e
	}
	return err
}

// snapshot 返回不会与后台刷新竞争的独立视图。
func (m *Manager) snapshot() View {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, _ := json.Marshal(m.view)
	var v View
	_ = json.Unmarshal(raw, &v)
	age := time.Since(v.CheckedAt)
	if v.Ready && (age < -5*time.Second || age > 30*time.Second) {
		v.Ready = false
		v.Error = "网络策略核验已过期，请检查宿主服务"
	}
	return v
}

// update 先校验新规则，再原子保存配置；落盘失败时恢复旧规则。
func (m *Manager) update(ctx context.Context, input Update) (View, error) {
	var result View
	err := m.withLock(func() error {
		c, err := Normalize(input.Config)
		if err != nil {
			return err
		}
		oldRaw, err := os.ReadFile(m.paths.Config)
		if err != nil {
			return ErrUnavailable
		}
		if input.ExpectedRevision == "" || hashBytes(oldRaw) != input.ExpectedRevision {
			return ErrConflict
		}
		interfaces, err := inventory(ctx)
		if err != nil {
			return ErrUnavailable
		}
		ranges, err := Ranges(c, interfaces)
		if err != nil {
			return err
		}
		_, exists, err := tableState(ctx)
		if err != nil {
			return ErrUnavailable
		}
		previous := m.view
		if err = m.apply(ctx, c, ranges, exists); err != nil {
			return ErrUnavailable
		}
		if err = atomicJSON(m.paths.Config, c, 0644); err != nil {
			if rollback := m.apply(ctx, previous.Config, previous.EffectiveBlockedCIDRs, true); rollback != nil {
				_ = m.failed(ErrUnavailable)
			}
			return ErrUnavailable
		}
		saved, err := os.ReadFile(m.paths.Config)
		if err != nil {
			return ErrUnavailable
		}
		m.desired, m.actual = "", ""
		m.view = View{Config: c, Revision: hashBytes(saved), Ready: true, CheckedAt: time.Now().UTC(), EffectiveBlockedCIDRs: ranges, Interfaces: interfaces}
		if err = m.publish(); err != nil {
			m.view.Ready = false
			return ErrUnavailable
		}
		result = m.view
		result.DNSChanged = strings.Join(previous.Config.DNSServers, ",") != strings.Join(c.DNSServers, ",")
		return nil
	})
	return result, err
}

// check 校验现有状态和自己的规则表，不重建任何规则。
func check(ctx context.Context, paths Paths) error {
	raw, err := os.ReadFile(filepath.Join(paths.State, "status.json"))
	if err != nil {
		return ErrUnavailable
	}
	var s Status
	if json.Unmarshal(raw, &s) != nil {
		return ErrUnavailable
	}
	age := time.Since(s.CheckedAt)
	if s.Version != 1 || !s.Ready || s.BridgePrefix != BridgePrefix || age < -5*time.Second || age > 30*time.Second {
		return ErrUnavailable
	}
	_, exists, err := tableState(ctx)
	if err != nil || !exists {
		return ErrUnavailable
	}
	return nil
}
