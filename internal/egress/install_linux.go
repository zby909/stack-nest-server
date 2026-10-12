package egress

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed stacknest-egress.service
var serviceUnit []byte

//go:embed docker-egress.conf
var dockerUnit []byte

type values []string

// String 显示重复参数，便于命令行帮助保持标准格式。
func (v *values) String() string { return strings.Join(*v, ",") }

// Set 收集多网卡、多网段和多个 DNS 参数。
func (v *values) Set(value string) error { *v = append(*v, value); return nil }

// atomicFile 原子写入安装文件，避免更新运行中的二进制 inode。
func atomicFile(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".stacknest-install-")
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
	return os.Rename(name, path)
}

// install 保留现有配置，安装内嵌 unit；升级 Python 版本时迁移原有策略。
func install(ctx context.Context, interfaces, blocked, dns []string) error {
	if os.Geteuid() != 0 {
		return errors.New("安装需要 root")
	}
	for _, name := range []string{"nft", "ip", "systemctl"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("请先安装 %s", name)
		}
	}
	paths := DefaultPaths()
	activeCmd := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "stacknest-egress.service")
	active := activeCmd.Run() == nil
	if _, err := os.Stat(paths.Config); errors.Is(err, os.ErrNotExist) {
		c := Config{LANInterfaces: interfaces, BlockedCIDRs: blocked, DNSServers: dns, Allow: []Exception{}}
		legacy := "/etc/stacknest-egress.json"
		if raw, err := os.ReadFile(legacy); err == nil {
			if len(interfaces)+len(blocked)+len(dns) > 0 {
				return errors.New("已有配置，请勿用安装参数覆盖")
			}
			if err = decode(strings.NewReader(string(raw)), &c); err != nil {
				return err
			}
			if err = atomicFile("/etc/stacknest-egress/legacy-config.json", raw, 0600); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		inventory, err := inventory(ctx)
		if err != nil {
			return err
		}
		if len(c.LANInterfaces) == 0 {
			for _, iface := range inventory {
				if iface.Eligible {
					c.LANInterfaces = append(c.LANInterfaces, iface.Name)
				}
			}
		}
		if len(c.DNSServers) == 0 {
			c.DNSServers = []string{"1.1.1.1", "8.8.8.8"}
		}
		c, err = Normalize(c)
		if err != nil {
			return err
		}
		if _, err = Ranges(c, inventory); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(paths.Config), 0755); err != nil {
			return err
		}
		if err = atomicJSON(paths.Config, c, 0644); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if len(interfaces)+len(blocked)+len(dns) > 0 {
		return errors.New("已有配置，请编辑配置文件或在超管页面修改")
	}
	source, err := os.Executable()
	if err != nil {
		return err
	}
	if source != "/usr/local/bin/stacknest-egress" {
		f, err := os.Open(source)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(f, 64<<20))
		f.Close()
		if err != nil {
			return err
		}
		if err = atomicFile("/usr/local/bin/stacknest-egress", raw, 0755); err != nil {
			return err
		}
	}
	if err = atomicFile("/etc/systemd/system/stacknest-egress.service", serviceUnit, 0644); err != nil {
		return err
	}
	m := &Manager{paths: paths}
	if err = m.synchronize(ctx); err != nil {
		return err
	}
	if _, err = command(ctx, "", "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if active {
		_, err = command(ctx, "", "systemctl", "restart", "stacknest-egress.service")
	} else {
		_, err = command(ctx, "", "systemctl", "enable", "--now", "stacknest-egress.service")
	}
	if err != nil {
		return err
	}
	if _, err = command(ctx, "", "systemctl", "enable", "stacknest-egress.service"); err != nil {
		return err
	}
	if err = atomicFile("/etc/systemd/system/docker.service.d/stacknest-egress.conf", dockerUnit, 0644); err != nil {
		return err
	}
	if _, err = command(ctx, "", "systemctl", "daemon-reload"); err != nil {
		return err
	}
	return check(ctx, paths)
}

// Run 处理固定安装、监听和核验命令；自定义路径仅供隔离测试和诊断。
func Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: stacknest-egress install|watch|check|sync")
	}
	action := args[0]
	fs := flag.NewFlagSet("stacknest-egress", flag.ContinueOnError)
	var interfaces, blocked, dns values
	fs.Var(&interfaces, "interface", "LAN interface (repeatable)")
	fs.Var(&blocked, "block-cidr", "additional LAN prefix (repeatable)")
	fs.Var(&dns, "dns", "public DNS server (repeatable)")
	defaults := DefaultPaths()
	config := fs.String("config", defaults.Config, "configuration path")
	state := fs.String("state", defaults.State, "runtime state directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	paths := Paths{Config: *config, State: *state}
	if action != "install" && len(interfaces)+len(blocked)+len(dns) > 0 {
		return errors.New("configuration flags only apply to first installation")
	}
	switch action {
	case "install":
		if paths != defaults {
			return errors.New("installation uses fixed production paths")
		}
		return install(ctx, interfaces, blocked, dns)
	case "watch":
		return watch(ctx, paths)
	case "sync":
		m := &Manager{paths: paths}
		return m.synchronize(ctx)
	case "check":
		return check(ctx, paths)
	default:
		return errors.New("unknown command")
	}
}
