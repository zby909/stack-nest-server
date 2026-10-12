package egress

import (
	"strings"
	"testing"
)

// baseConfig 构造可访问公网但保护家庭网络的策略。
func baseConfig() Config {
	return Config{LANInterfaces: []string{"eth0"}, BlockedCIDRs: []string{"192.168.2.0/24"}, Allow: []Exception{}, DNSServers: []string{"1.1.1.1", "223.5.5.5"}}
}

// TestRejectUnsafeConfiguration 覆盖未知地址、宽网段和命令注入边界。
func TestRejectUnsafeConfiguration(t *testing.T) {
	tests := []func(*Config){
		func(c *Config) { c.LANInterfaces = []string{`eth0"; flush ruleset`} },
		func(c *Config) { c.BlockedCIDRs = []string{"0.0.0.0/0"} },
		func(c *Config) { c.BlockedCIDRs = []string{"192.168.2.10/24"} },
		func(c *Config) { c.BlockedCIDRs = []string{"fe80::%eth0/64"} },
		func(c *Config) {
			c.Allow = []Exception{{Address: "fe80::1%eth0; flush ruleset", Port: 80, Protocol: "tcp"}}
		},
		func(c *Config) { c.Allow = []Exception{{Address: "192.168.2.3", Port: 0, Protocol: "tcp"}} },
		func(c *Config) { c.Allow = []Exception{{Address: "192.168.2.3", Port: 53, Protocol: "icmp"}} },
		func(c *Config) { c.DNSServers = []string{"192.168.2.1"} },
		func(c *Config) { c.DNSServers = []string{"100.68.110.44"} },
		func(c *Config) { c.DNSServers = []string{"224.0.0.1"} },
		func(c *Config) { c.DNSServers = []string{"fe80::1%eth0"} },
	}
	for i, change := range tests {
		c := baseConfig()
		change(&c)
		if _, err := Normalize(c); err == nil {
			t.Errorf("unsafe config %d accepted", i)
		}
	}
}

// TestRangesAndRules 验证真实网段范围及宿主、入站响应和单端口例外的优先顺序。
func TestRangesAndRules(t *testing.T) {
	c := baseConfig()
	c.Allow = []Exception{{Address: "192.168.2.3", Port: 5432, Protocol: "tcp"}}
	c, err := Normalize(c)
	if err != nil {
		t.Fatal(err)
	}
	interfaces := []Interface{{Name: "eth0", Addresses: []string{"172.21.47.248/20", "fe80::2/64"}}, {Name: "docker0", Addresses: []string{"172.17.0.1/16"}}, {Name: "tailscale0", Addresses: []string{"100.68.110.44/32"}}}
	ranges, err := Ranges(c, interfaces)
	if err != nil {
		t.Fatal(err)
	}
	text := Render(c, ranges, true)
	for _, value := range []string{`iifname "sne-*"`, "priority -110", "ct direction reply", "172.21.32.0/20", "192.168.2.0/24", "fe80::/64", "192.168.2.3 tcp dport 5432 return"} {
		if !strings.Contains(text, value) {
			t.Errorf("missing rule: %s", value)
		}
	}
	for _, value := range []string{"flush ruleset", "172.17.0.0/16", "100.64.0.0/10", "DOCKER-USER"} {
		if strings.Contains(text, value) {
			t.Errorf("unintended scope: %s", value)
		}
	}
	if strings.Index(text, "fib daddr type local return") > strings.Index(text, "ip daddr @lan4") {
		t.Fatal("host allow is too late")
	}
	if _, err = Ranges(c, nil); err == nil {
		t.Fatal("missing interface accepted")
	}
}

// TestCountersDoNotTriggerReload 确保规则计数和句柄变化不会反复重建规则。
func TestCountersDoNotTriggerReload(t *testing.T) {
	a := []byte(`{"nftables":[{"metainfo":{"time":1}},{"rule":{"handle":2,"expr":[{"counter":{"packets":1,"bytes":40}}]}}]}`)
	b := []byte(`{"nftables":[{"metainfo":{"time":2}},{"rule":{"handle":3,"expr":[{"counter":{"packets":100,"bytes":4000}}]}}]}`)
	one, err := ruleHash(a)
	if err != nil {
		t.Fatal(err)
	}
	two, err := ruleHash(b)
	if err != nil {
		t.Fatal(err)
	}
	if one != two {
		t.Fatal("dynamic counters change fingerprint")
	}
}

// TestDNSPreference 验证 DNS 去重不会改变部署者指定的首选顺序。
func TestDNSPreference(t *testing.T) {
	c, err := Normalize(Config{LANInterfaces: []string{"eth0"}, DNSServers: []string{"223.5.5.5", "1.1.1.1", "223.5.5.5"}})
	if err != nil || strings.Join(c.DNSServers, ",") != "223.5.5.5,1.1.1.1" {
		t.Fatalf("DNS preference lost: %#v %v", c.DNSServers, err)
	}
}
