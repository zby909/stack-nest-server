package dockerrunner

import (
	"context"
	"net/url"
	"path/filepath"
	"stacknest/server/internal/containerapp"
	"time"
)

// quotaReader 将只读计量能力与配额分配分离，读取不修改目录或限制。
type quotaReader interface {
	Usage(id, dir string) (containerapp.DiskArea, error)
	WritableUsage(upper, dockerRoot string) (containerapp.DiskArea, error)
}
type diskSample struct {
	value   *containerapp.DiskUsage
	started time.Time
	loading bool
}

// diskUsage 后台低频采样，慢速 Docker 计量不会拖住列表和健康状态。
func (r *Runner) diskUsage(id string, value inspection) *containerapp.DiskUsage {
	r.diskMu.Lock()
	defer r.diskMu.Unlock()
	if r.disk == nil {
		r.disk = make(map[string]*diskSample)
	}
	entry := r.disk[value.ID]
	if entry == nil {
		// 只保留近期访问的容器，容器更新不会复用旧版计数。
		for key, old := range r.disk {
			if !old.loading && time.Since(old.started) > time.Minute {
				delete(r.disk, key)
			}
		}
		if len(r.disk) >= 128 {
			return nil
		}
		entry = &diskSample{}
		r.disk[value.ID] = entry
	}
	if !entry.loading && time.Since(entry.started) >= time.Minute && r.diskActive < 2 {
		entry.loading = true
		entry.started = time.Now()
		r.diskActive++
		go func() {
			sample := r.sampleDisk(id, value)
			r.diskMu.Lock()
			entry.value, entry.loading = sample, false
			r.diskActive--
			r.diskMu.Unlock()
		}()
	}
	return entry.value
}

// sampleDisk 优先读取 XFS 实际配额计数，未启用时回退为明确标注的 Docker 文件大小估计。
func (r *Runner) sampleDisk(id string, value inspection) *containerapp.DiskUsage {
	sample := &containerapp.DiskUsage{Writable: containerapp.DiskArea{Source: "unavailable"}}
	reader, enabled := r.quota.(quotaReader)
	if enabled {
		if area, err := reader.WritableUsage(value.GraphDriver.Data["UpperDir"], r.dockerRoot); err == nil {
			sample.Writable = area
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var measured struct{ SizeRw *uint64 }
		if err := r.engine.call(ctx, "GET", "/containers/"+url.PathEscape(value.ID)+"/json?size=1", nil, &measured); err == nil && measured.SizeRw != nil {
			sample.Writable = containerapp.DiskArea{UsedBytes: measured.SizeRw, Source: "docker-estimate"}
		}
	}
	for _, mount := range value.Mounts {
		if mount.Type == "bind" && mount.Source == filepath.Join(r.source, "apps", id, "data") {
			area := containerapp.DiskArea{Source: "unavailable"}
			if enabled {
				if measured, err := reader.Usage(id, filepath.Join(r.dataDir, "apps", id, "data")); err == nil {
					area = measured
				}
			} else {
				area.Source = "quota-disabled"
			}
			sample.Persistent = &area
			break
		}
	}
	sample.SampledAt = time.Now().UTC()
	return sample
}
