package traffic

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestDailyCounts 验证错误、页面、UTC 跨日、同名新项目与数据保留边界。
func TestDailyCounts(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 5, 0, time.UTC)
	host := "stack-nest-site.example.test"
	c := collector{routes: Targets{host: {"site:two", now.Add(-10 * time.Second)}}, state: snapshot{Since: now.AddDate(0, 0, -9), Days: map[string]map[string]Counts{"2026-11-13": {"site:two": {Requests: 40}}}}}
	message := func(at time.Time, host string, status, page int) string {
		return fmt.Sprintf("<190>Nov 20 00:00:05 stacknest: sn1 %d.000 %s %d %d", at.Unix(), host, status, page)
	}
	for _, entry := range []string{message(now, host, 200, 1), message(now, host, 404, 0), message(now, host, 502, 1), message(now.Add(-7*time.Second), host, 304, 0), message(now.Add(-11*time.Second), host, 200, 1), message(now, "unlisted.example", 500, 0)} {
		c.record(entry, now)
	}
	dir := t.TempDir()
	if err := c.flush(filepath.Join(dir, "traffic-counts.json"), now); err != nil {
		t.Fatal(err)
	}
	summary, err := Read(dir, "site:two", now)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Available || len(summary.Days) != 7 || summary.Days[5].Requests != 1 || summary.Days[6].Counts != (Counts{Requests: 3, Pages: 2, ClientErrors: 1, ServerErrors: 1}) || len(c.state.Days) != 2 {
		t.Fatalf("bad summary: %+v", summary)
	}
	other, _ := Read(dir, "site:one", now)
	if other.Days[6].Requests != 0 {
		t.Fatal("cross-project counts")
	}
}

// TestCollectorPersists 验证真实 Unix 数据报、退出落盘及 Runner 重启后累加。
func TestCollectorPersists(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux Nginx socket")
	}
	dir, err := os.MkdirTemp("", "sn-traffic-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	reg := Registry{Dir: dir, BaseDomain: "example.test", Load: func(context.Context) (Targets, error) {
		return Targets{"stack-nest-demo": {"site:id", time.Now().Add(-time.Hour)}}, nil
	}}
	if err = reg.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for pass := int64(1); pass <= 2; pass++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Collect(ctx, dir) }()
		var socket *net.UnixConn
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			socket, err = net.DialUnix("unixgram", nil, &net.UnixAddr{Name: filepath.Join(dir, "traffic.sock"), Net: "unixgram"})
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(socket, "<190>Nov 20 00:00:00 stacknest: sn1 %d.000 stack-nest-demo.example.test 200 1", time.Now().Unix())
		socket.Close()
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		time.Sleep(80 * time.Millisecond)
		cancel()
		if err = <-done; err != nil {
			t.Fatal(err)
		}
		summary, err := Read(dir, "site:id", time.Now())
		if err != nil || summary.Days[6].Pages != pass {
			t.Fatalf("restart %d: %+v %v", pass, summary, err)
		}
	}
}
