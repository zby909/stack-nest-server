package dockerrunner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"stacknest/server/internal/containerapp"
)

type failingDataQuota struct{}

// Ensure 模拟配额设置失败，不允许降级启动。
func (failingDataQuota) Ensure(_, _ string) error { return errors.New("quota unavailable") }

// Remove 模拟安全清理失败，交由调用者重试。
func (failingDataQuota) Remove(_, _ string) error { return errors.New("quota unavailable") }

// TestDataQuotaFailurePreventsCreate 验证配额失败不会向 Docker 发出创建请求。
func TestDataQuotaFailurePreventsCreate(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		w.WriteHeader(500)
	}))
	defer server.Close()
	r := &Runner{engine: fakeEngine(server), dataDir: t.TempDir(), quota: failingDataQuota{}}
	op := containerapp.Operation{ID: "0c2d84bb-15a1-492b-938a-83282600c112", Generation: 1, Spec: containerapp.Spec{DataPath: "/app/data"}}
	if _, err := r.create(context.Background(), op, imageInfo{}); err == nil || calls != 0 {
		t.Fatal("quota error must prevent Docker create", err, calls)
	}
}

// TestInvalidDataQuotaConfig 验证错误配置不会静默启用不限额模式。
func TestInvalidDataQuotaConfig(t *testing.T) {
	for _, value := range []string{"-1", "15", "32769", "not-a-number"} {
		t.Setenv("STACKNEST_APP_DATA_LIMIT_MIB", value)
		if err := (&Runner{}).configureDataQuota(""); err == nil {
			t.Fatal("invalid quota accepted", value)
		}
	}
}

// TestQuotaSeccompProfile 验证 profile 仍使用拒绝默认动作，并封锁配额属性及 chattr 修改。
func TestQuotaSeccompProfile(t *testing.T) {
	var profile struct {
		DefaultAction string
		Syscalls      []struct {
			Names    []string
			Action   string
			ErrnoRet int
			Args     []struct {
				Index           int
				Value, ValueTwo uint64
				Op              string
			}
		}
	}
	if err := json.Unmarshal([]byte(quotaSeccomp), &profile); err != nil {
		t.Fatal(err)
	}
	blocked := map[uint64]bool{0x401c5820: false, 0x40086602: false, 0x40046602: false}
	for _, rule := range profile.Syscalls {
		if len(rule.Names) == 1 && rule.Names[0] == "ioctl" && rule.Action == "SCMP_ACT_ERRNO" && rule.ErrnoRet == 13 && len(rule.Args) == 1 {
			arg := rule.Args[0]
			if arg.Index == 1 && arg.Op == "SCMP_CMP_MASKED_EQ" && arg.Value == 0xffffffff {
				blocked[arg.ValueTwo] = true
			}
		}
	}
	if profile.DefaultAction != "SCMP_ACT_ERRNO" {
		t.Fatal("default syscall policy weakened")
	}
	for command, value := range blocked {
		if !value {
			t.Fatalf("quota-changing ioctl %#x permitted", command)
		}
	}
}

// TestQuotaCreateIncludesSeccomp 即使应用不挂载数据目录，也不能移除可写层的配额归属。
func TestQuotaCreateIncludesSeccomp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			HostConfig struct{ SecurityOpt []string }
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload.HostConfig.SecurityOpt) != 2 || !strings.HasPrefix(payload.HostConfig.SecurityOpt[1], "seccomp={") {
			t.Error("quota protection missing")
		}
		_, _ = w.Write([]byte("{\"Id\":\"aaaaaaaaaaaa\"}"))
	}))
	defer server.Close()
	r := &Runner{engine: fakeEngine(server), quota: failingDataQuota{}, dataDir: t.TempDir()}
	op := containerapp.Operation{ID: "0c2d84bb-15a1-492b-938a-83282600c112", Generation: 1}
	if _, err := r.create(context.Background(), op, imageInfo{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.bind("../other", containerapp.Spec{DataPath: filepath.Join("/app", "data")}); err == nil {
		t.Fatal("invalid app ID accepted")
	}
}
