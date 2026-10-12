package dockerrunner

import (
	"net/http"
	"stacknest/server/internal/containerapp"
)

// capacityBudget 为宿主及平台至少预留 832 MiB 和四分之一内存。
func capacityBudget(bytes int64) int {
	total := int(bytes >> 20)
	budget := min(total-832, total*3/4, 131072)
	return max(budget, 0)
}

// budget 在测试替身和旧调用中沿用升级前的预算。
func (r *Runner) budget() int {
	if r.maxTotalMemoryMiB > 0 {
		return r.maxTotalMemoryMiB
	}
	return containerapp.TotalMemoryMiB
}

// capacity 只返回受限预算，不暴露 Docker inspect 或宿主环境。
func (r *Runner) capacity(w http.ResponseWriter, req *http.Request) {
	respond(w, 200, containerapp.Capacity{MaxTotalMemoryMiB: r.budget()})
}

// operationLimits 对可信后台传来的预算再次校验，旧内部请求使用默认值。
func (r *Runner) operationLimits(op containerapp.Operation) (containerapp.Limits, error) {
	value := containerapp.DefaultLimits()
	if op.Limits != nil {
		value = *op.Limits
	}
	if err := value.Validate(); err != nil {
		return value, err
	}
	if value.TotalMemoryMiB > r.budget() {
		return value, containerapp.ErrQuota
	}
	return value, nil
}
