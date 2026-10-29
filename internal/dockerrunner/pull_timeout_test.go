package dockerrunner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/storage"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type pullTransport func(*http.Request) (*http.Response, error)

// RoundTrip 用可控的内存流代替真实 Docker 下载，虚拟时钟可测试完整的一小时。
func (f pullTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestPullTimeouts 测试慢速推进、重复消息、解压、总时限和请求头等待，避免真实等待数十分钟。
func TestPullTimeouts(t *testing.T) {
	for _, scenario := range []string{"slow-download-and-extract", "stalled-stream", "stalled-headers", "total-limit", "interrupted-stream"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				guard := storage.New(t.TempDir())
				guard.Read = func(string) (storage.Usage, error) {
					return storage.Usage{TotalBytes: 40 << 30, FreeBytes: 30 << 30}, nil
				}
				inspected := false
				engine := &Engine{client: &http.Client{Transport: pullTransport(func(req *http.Request) (*http.Response, error) {
					if req.Method == "GET" {
						inspected = true
						return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"Id":"image","Os":"linux","Architecture":"amd64","Size":1024}`))}, nil
					}
					if scenario == "stalled-headers" {
						<-req.Context().Done()
						return nil, req.Context().Err()
					}
					reader, writer := io.Pipe()
					stop := context.AfterFunc(req.Context(), func() { _ = reader.CloseWithError(req.Context().Err()) })
					go func() {
						defer stop()
						defer writer.Close()
						encoder := json.NewEncoder(writer)
						for minute := 0; minute < 120; minute++ {
							event := pullEvent{ID: "layer", Status: "Downloading"}
							event.ProgressDetail.Current, event.ProgressDetail.Total = int64(minute+1), 1000
							if scenario == "slow-download-and-extract" && minute >= 12 {
								event.Status = "Extracting"
								event.ProgressDetail.Current = int64(minute - 11)
							}
							if scenario == "stalled-stream" {
								event.ProgressDetail.Current = 1
							}
							if encoder.Encode(event) != nil {
								return
							}
							if scenario == "interrupted-stream" {
								_ = writer.CloseWithError(io.ErrUnexpectedEOF)
								return
							}
							if scenario == "slow-download-and-extract" && minute == 20 {
								return
							}
							select {
							case <-req.Context().Done():
								return
							case <-time.After(time.Minute):
							}
						}
					}()
					return &http.Response{StatusCode: 200, Body: reader}, nil
				})}}
				r := &Runner{engine: engine, guard: guard}
				started := time.Now()
				_, err := r.pull(context.Background(), containerapp.Spec{Image: "example/app:v1"}, "")
				elapsed := time.Since(started)
				switch scenario {
				case "slow-download-and-extract":
					if err != nil || !inspected || elapsed != 20*time.Minute {
						t.Fatalf("slow active pull failed: %v %v %v", err, inspected, elapsed)
					}
				case "stalled-stream", "stalled-headers":
					if !errors.Is(err, errPullIdle) || inspected || elapsed < pullIdleTimeout || elapsed > pullIdleTimeout+time.Second {
						t.Fatalf("idle pull not stopped precisely: %v %v %v", err, inspected, elapsed)
					}
				case "total-limit":
					if !errors.Is(err, errPullTimeout) || inspected || elapsed != containerapp.ImagePullTimeout {
						t.Fatalf("total cap lost: %v %v %v", err, inspected, elapsed)
					}
				case "interrupted-stream":
					if err == nil || !strings.Contains(err.Error(), "镜像拉取中断") || inspected {
						t.Fatalf("stream failure misreported: %v", err)
					}
				}
			})
		})
	}
}

// TestPullWatchCancellation 验证关闭和磁盘不足保留准确原因，释放监视器后不再检查磁盘。
func TestPullWatchCancellation(t *testing.T) {
	for _, reason := range []string{"shutdown", "disk", "finished"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, shutdown := context.WithCancel(context.Background())
				defer shutdown()
				calls := 0
				ctx, _, finish := watchPull(parent, func() error { calls++; return errors.New("private disk detail") })
				defer finish()
				want := context.Canceled
				switch reason {
				case "shutdown":
					shutdown()
				case "finished":
					finish()
				case "disk":
					want = errPullDisk
				}
				<-ctx.Done()
				synctest.Wait()
				if !errors.Is(context.Cause(ctx), want) {
					t.Fatal(context.Cause(ctx))
				}
				before := calls
				time.Sleep(time.Hour)
				if calls != before || reason == "disk" && calls != 1 {
					t.Fatalf("watchdog leaked: %d -> %d", before, calls)
				}
			})
		})
	}
}

// TestPullActivityRetries 验证重试重新下载可推进，而等待、倒退和未知文本不会不断续期。
func TestPullActivityRetries(t *testing.T) {
	layers := pullActivity{}
	for _, step := range []struct {
		status   string
		current  int64
		advances bool
	}{
		{"Downloading", 20, true}, {"Downloading", 20, false}, {"Downloading", 21, true},
		{"Downloading", 0, false}, {"Retrying in 5 seconds", 0, false}, {"Downloading", 1, true},
		{"Download complete", 100, true}, {"Download complete", 100, false},
		{"Extracting", 1, true}, {"Extracting", 2, true}, {"Extracting", 2, false},
		{"Waiting", 0, true}, {"Waiting", 0, false}, {"Extracting", 2, false},
		{"Pull complete", 100, true}, {"Pull complete", 100, false},
	} {
		event := pullEvent{ID: "layer", Status: step.status}
		event.ProgressDetail.Current, event.ProgressDetail.Total = step.current, 100
		if layers.observe(event) != step.advances {
			t.Fatalf("unexpected activity: %+v", step)
		}
	}
}

// TestOperationDeadlineReportedByRunner 验证运行器自己终止卡住的操作，向后台返回明确的安全错误。
func TestOperationDeadlineReportedByRunner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &Runner{engine: &Engine{client: &http.Client{Transport: pullTransport(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})}}}
		_, err := r.operate(context.Background(), containerapp.Operation{ID: "0c2d84bb-15a1-492b-938a-83282600c112", Generation: 1, Action: "deploy"})
		if !errors.Is(err, containerapp.ErrOperationTimeout) {
			t.Fatal(err)
		}
	})
}
