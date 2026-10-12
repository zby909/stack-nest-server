package dockerrunner

import (
	"context"
	"net/http"
	"stacknest/server/internal/containerapp"
	"time"
)

// diagnostics 重新确认 Docker、项目配额和网络保护就绪，不创建测试资源。
func (r *Runner) diagnostics(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), 4*time.Second)
	defer cancel()
	var info struct{ Driver string }
	if r.engine.call(ctx, "GET", "/info", nil, &info) != nil || r.checkEgress() != nil {
		respond(w, 503, map[string]string{"error": "运行器依赖未就绪"})
		return
	}
	value := containerapp.Diagnostics{Driver: info.Driver, NetworkProtected: r.egressFile != ""}
	if r.quota != nil {
		checker, ok := r.quota.(interface{ Check() error })
		if !ok || checker.Check() != nil {
			respond(w, 503, map[string]string{"error": "XFS 项目配额检查失败"})
			return
		}
		value.DataQuota = true
		value.WritableQuota = info.Driver == "overlay2"
	}
	respond(w, 200, value)
}
