package dockerrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/storage"
)

const instanceLabel = "io.stacknest.instance"
const appLabel = "io.stacknest.app"
const portLabel = "io.stacknest.port"
const generationLabel = "io.stacknest.generation"
const healthPathLabel = "io.stacknest.health-path"

type result = containerapp.Result

// Runner 持有 Docker 控制权，不监听 TCP，不接受任意 bind 或 Docker 参数。
type Runner struct {
	engine                            *Engine
	selfID, instance, source, dataDir string
	guard                             *storage.Guard
	quota                             dataQuota
	mu                                sync.Mutex
	frozen                            bool
}

var hexID = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

// New 根据本容器的数据卷生成实例身份，多个平台实例不会误操作对方容器。
func New(ctx context.Context, socket, dataDir, dockerStorage string) (*Runner, error) {
	hostname, err := os.Hostname()
	if err != nil || !hexID.MatchString(hostname) {
		return nil, errors.New("运行器必须在 Docker 容器中启动")
	}
	r := &Runner{engine: NewEngine(socket), selfID: hostname, dataDir: dataDir, guard: storage.New(dockerStorage)}
	self, err := r.engine.inspect(ctx, hostname)
	if err != nil {
		return nil, err
	}
	for _, mount := range self.Mounts {
		if mount.Destination == dataDir && mount.Type == "volume" && mount.Name != "" {
			digest := sha256.Sum256([]byte(mount.Name))
			r.instance = hex.EncodeToString(digest[:6])
			r.source = mount.Source
		}
	}
	if r.source == "" {
		return nil, errors.New("运行器需要独立的 StackNest 命名数据卷")
	}
	var info struct {
		MemTotal int64
		OSType   string
	}
	if err = r.engine.call(ctx, "GET", "/info", nil, &info); err != nil {
		return nil, err
	}
	if info.OSType != "linux" || info.MemTotal < (containerapp.TotalMemoryMiB*2+64)<<20 {
		return nil, errors.New("Docker 应用运行需要 Linux 容器和至少 1600 MiB 内存")
	}
	if err = os.MkdirAll(filepath.Join(dataDir, "apps"), 0700); err != nil {
		return nil, err
	}
	if err = r.configureDataQuota(dockerStorage); err != nil {
		return nil, err
	}
	if _, err = os.Stat(filepath.Join(dataDir, "app-maintenance.json")); err == nil {
		r.frozen = true
	}
	values, err := r.containers(ctx, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, value := range values {
		id := value.Labels[appLabel]
		if !seen[id] && containerapp.ValidID(id) {
			if r.quota != nil {
				if _, err = r.owned(ctx, id, value.ID); err != nil {
					return nil, errors.New("启用数据硬配额前须停止并重建旧应用容器（保留数据目录）")
				}
			}
			if err = r.ensureNetwork(ctx, id); err != nil {
				return nil, err
			}
			seen[id] = true
		}
	}
	return r, nil
}

// Serve 在仅平台用户可访问的 Unix socket 上提供服务，退出时不触碰应用数据。
func (r *Runner) Serve(ctx context.Context, socket string) error {
	if err := os.MkdirAll(filepath.Dir(socket), 0750); err != nil {
		return err
	}
	if err := os.Chown(filepath.Dir(socket), 10001, 10001); err != nil {
		return err
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("运行器 socket 路径被占用")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chown(socket, 10001, 10001); err != nil {
		return err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, req *http.Request) {
		check, cancel := context.WithTimeout(req.Context(), 3*time.Second)
		defer cancel()
		if err := r.engine.call(check, "GET", "/_ping", nil, nil); err != nil {
			http.Error(w, "Docker 不可用", 503)
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /operation", r.operation)
	mux.HandleFunc("GET /state/{id}/{container}", r.state)
	mux.HandleFunc("GET /logs/{id}/{container}", r.logs)
	mux.HandleFunc("/proxy/{id}/{container}/{path...}", r.proxy)
	mux.HandleFunc("POST /maintenance", r.maintenance)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// respond 输出限定结构，不输出配置正文。
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// operation 串行执行固定操作，后台备份冻结期间拒绝新任务。
func (r *Runner) operation(w http.ResponseWriter, req *http.Request) {
	var op containerapp.Operation
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, containerapp.ConfigJSONLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&op); err != nil || !containerapp.ValidID(op.ID) || op.Generation < 1 {
		respond(w, 400, map[string]string{"error": "应用操作参数无效"})
		return
	}
	if err := op.Spec.Validate(); err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	switch op.Action {
	case "deploy", "start", "stop", "restart", "delete":
	default:
		respond(w, 400, map[string]string{"error": "应用操作无效"})
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		respond(w, 503, map[string]string{"error": "备份或迁移期间暂停应用操作"})
		return
	}
	value, err := r.operate(req.Context(), op)
	if err != nil {
		respond(w, 409, map[string]any{"error": err.Error(), "result": value})
		return
	}
	respond(w, 200, value)
}

// owned 校验标签与固定私有网络，外部构造的编号不能触碰平台容器。
func (r *Runner) owned(ctx context.Context, id, cid string) (inspection, error) {
	if !containerapp.ValidID(id) || !hexID.MatchString(cid) {
		return inspection{}, errMissing
	}
	value, err := r.engine.inspect(ctx, cid)
	if err != nil {
		return value, err
	}
	if value.Config.Labels[instanceLabel] != r.instance || value.Config.Labels[appLabel] != id || value.HostConfig.Privileged || value.HostConfig.NetworkMode != r.networkName(id) {
		return inspection{}, errMissing
	}
	if r.quota != nil && !protectedQuotaPolicy(value.HostConfig.SecurityOpt) {
		return inspection{}, errors.New("应用容器缺少配额属性保护，需要重建")
	}
	return value, nil
}

// state 只提供已绑定应用的运行状态。
func (r *Runner) state(w http.ResponseWriter, req *http.Request) {
	value, err := r.owned(req.Context(), req.PathValue("id"), req.PathValue("container"))
	if err != nil {
		respond(w, 404, map[string]string{"error": "应用容器不存在"})
		return
	}
	response := stateResponse(value)
	response.HealthCheck = "tcp"
	if value.Config.Labels[healthPathLabel] != "" {
		response.HealthCheck = "http"
	}
	response.Health = "stopped"
	if value.State.Running {
		response.Health = "unhealthy"
		if r.probe(req.Context(), value, req.PathValue("id")) == nil {
			response.Health = "healthy"
		}
		response.Usage, _ = r.usage(req.Context(), value.ID)
	}
	respond(w, 200, response)
}
