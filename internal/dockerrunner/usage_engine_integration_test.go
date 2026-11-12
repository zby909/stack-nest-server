package dockerrunner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestUsageEngineIntegration 验证真实 Engine 首次返回内存，后续采样能计算 CPU 增量。
func TestUsageEngineIntegration(t *testing.T) {
	socket := os.Getenv("STACKNEST_TEST_DOCKER_SOCKET")
	if socket == "" {
		t.Skip("explicit Docker test socket required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	name := "stacknest-usage-test-" + uuid.NewString()
	image := name + ":fixture"
	work := t.TempDir()
	repo := filepath.Join("..", "..")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", filepath.Join(work, "smoke-http"), "./deploy/smoke-http")
	build.Dir = repo
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture binary: %v %s", err, out)
	}
	// docker 限定使用显式提供的测试 socket，夹具资源均带独立名称。
	docker := func(ctx context.Context, args ...string) (string, error) {
		command := exec.CommandContext(ctx, "docker", append([]string{"-H", "unix://" + socket}, args...)...)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		out, err := command.Output()
		if err != nil {
			out = append(out, stderr.Bytes()...)
		}
		return strings.TrimSpace(string(out)), err
	}
	if out, err := docker(ctx, "build", "--network=none", "-q", "-t", image,
		"-f", filepath.Join(repo, "deploy", "smoke-http", "Dockerfile"), work); err != nil {
		t.Fatalf("fixture image: %v %s", err, out)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		_, _ = docker(cleanup, "rm", "-f", name)
		if out, err := docker(cleanup, "image", "rm", image); err != nil {
			t.Errorf("fixture cleanup: %v %s", err, out)
		}
	})
	cid, err := docker(ctx, "run", "-d", "--name", name, "--network=none", "--pull=never", "--memory=64m",
		"--pids-limit=32", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", image)
	if err != nil {
		t.Fatalf("fixture start: %v %s", err, cid)
	}
	runner := &Runner{engine: NewEngine(socket)}
	value, err := runner.engine.inspect(ctx, cid)
	if err != nil || !value.State.Running || value.State.StartedAt.IsZero() {
		t.Fatalf("fixture has not started: %v", err)
	}
	first, err := runner.usage(ctx, cid, value.State.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if first.MemoryBytes == 0 || first.MemoryLimitBytes != 64<<20 || first.PIDs == 0 || first.CPUPercent != nil {
		t.Fatalf("unexpected first statistics: %+v", first)
	}
	check, done := context.WithTimeout(ctx, 3*time.Second)
	defer done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-check.Done():
			t.Fatal("real Docker CPU counters did not advance")
		case <-ticker.C:
			usage, err := runner.usage(check, cid, value.State.StartedAt)
			if err != nil {
				t.Fatal(err)
			}
			if usage.CPUPercent != nil && usage.SampledAt.After(first.SampledAt) {
				return
			}
		}
	}
}
