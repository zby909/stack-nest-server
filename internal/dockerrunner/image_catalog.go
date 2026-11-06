package dockerrunner

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"stacknest/server/internal/containerapp"
)

// catalogImage 只记录本实例新拉取的镜像，安装前已有镜像不自动归入清理范围。
type catalogImage struct {
	ID         string    `json:"id"`
	Reference  string    `json:"reference"`
	RecordedAt time.Time `json:"recordedAt"`
}

// imageCatalogPath 以已校验的内容编号生成数据卷内的固定路径。
func (r *Runner) imageCatalogPath(id string) string {
	return filepath.Join(r.dataDir, "app-images", strings.TrimPrefix(id, "sha256:")+".json")
}

// catalogEntry 拒绝损坏或非普通文件记录，无法确认时禁止清理。
func (r *Runner) catalogEntry(id string) (catalogImage, bool) {
	var value catalogImage
	if r.dataDir == "" || !containerapp.ValidImageID(id) {
		return value, false
	}
	path := r.imageCatalogPath(id)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return value, false
	}
	raw, err := os.ReadFile(path)
	valid := err == nil && json.Unmarshal(raw, &value) == nil && value.ID == id
	return value, valid
}

// recordPulledImage 原子记录新增镜像，记录失败仅取消清理资格，不影响已拉取的部署。
func (r *Runner) recordPulledImage(value imageInfo, reference string, before map[string]bool) {
	if r.dataDir == "" || !containerapp.ValidImageID(value.ID) || before == nil || before[value.ID] {
		return
	}
	if _, exists := r.catalogEntry(value.ID); exists {
		return
	}
	dir := filepath.Dir(r.imageCatalogPath(value.ID))
	err := os.MkdirAll(dir, 0700)
	if err == nil {
		var file *os.File
		file, err = os.CreateTemp(dir, ".image-")
		if err == nil {
			defer os.Remove(file.Name())
			defer file.Close()
			err = json.NewEncoder(file).Encode(catalogImage{value.ID, reference, time.Now().UTC()})
			if err == nil {
				err = file.Sync()
			}
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err == nil {
				err = os.Rename(file.Name(), r.imageCatalogPath(value.ID))
			}
		}
	}
	if err != nil {
		slog.Error("record pulled image", "error", err)
	}
}

// localImageIDs 在拉取前记住已有镜像，失败时返回 nil 并保守跳过登记。
func (r *Runner) localImageIDs(ctx context.Context) map[string]bool {
	var values []struct {
		ID string `json:"Id"`
	}
	if r.engine.call(ctx, "GET", "/images/json?all=1", nil, &values) != nil {
		return nil
	}
	ids := map[string]bool{}
	for _, value := range values {
		ids[value.ID] = true
	}
	return ids
}

// imageInventory 查询共享层合计，并核对全部容器及 API 提供的应用配置引用。
func (r *Runner) imageInventory(ctx context.Context, refs []string) (containerapp.ImageInventory, error) {
	var disk struct {
		LayersSize int64
		Images     []struct {
			ID          string `json:"Id"`
			RepoTags    []string
			RepoDigests []string
			Size        int64
			Containers  int64
		}
	}
	if err := r.engine.call(ctx, "GET", "/system/df?type=image", nil, &disk); err != nil {
		return containerapp.ImageInventory{}, err
	}
	var containers []struct{ ImageID string }
	if err := r.engine.call(ctx, "GET", "/containers/json?all=1", nil, &containers); err != nil {
		return containerapp.ImageInventory{}, err
	}
	protected := map[string]bool{}
	for _, value := range containers {
		protected[value.ImageID] = true
	}
	for _, ref := range refs {
		var info imageInfo
		err := r.engine.call(ctx, "GET", "/images/"+url.PathEscape(ref)+"/json", nil, &info)
		if errors.Is(err, errMissing) {
			continue
		}
		if err != nil {
			return containerapp.ImageInventory{}, err
		}
		protected[info.ID] = true
	}
	result := containerapp.ImageInventory{TotalBytes: disk.LayersSize, Images: []containerapp.ImageUsage{}}
	for _, value := range disk.Images {
		_, managed := r.catalogEntry(value.ID)
		tags := value.RepoTags
		if tags == nil {
			tags = []string{}
		}
		item := containerapp.ImageUsage{ID: value.ID, Tags: tags, SizeBytes: value.Size, Managed: managed}
		switch {
		case protected[value.ID] || value.Containers > 0:
			item.Reason = "应用或容器正在引用"
		case !managed:
			item.Reason = "无本实例新增拉取记录"
		case len(value.RepoTags) > 1 || len(value.RepoDigests) > 1:
			item.Reason = "存在多个镜像引用，需人工检查"
		default:
			item.Removable, item.Reason = true, "可清理"
		}
		result.Images = append(result.Images, item)
	}
	sort.Slice(result.Images, func(i, j int) bool { return result.Images[i].SizeBytes > result.Images[j].SizeBytes })
	return result, nil
}

// manageImages 与部署和备份共用锁，删除前重查清单且绝不强制解除引用。
func (r *Runner) manageImages(w http.ResponseWriter, req *http.Request) {
	var input containerapp.ImageRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 128<<10))
	decoder.DisallowUnknownFields()
	deleting := req.URL.Path == "/images/delete"
	if decoder.Decode(&input) != nil || len(input.Protected) > 400 || deleting && !containerapp.ValidImageID(input.ID) {
		respond(w, 400, map[string]string{"error": "镜像参数无效"})
		return
	}
	for _, ref := range input.Protected {
		if len(ref) > 256 || ref == "" {
			respond(w, 400, map[string]string{"error": "镜像引用无效"})
			return
		}
	}
	if !r.mu.TryLock() {
		respond(w, 409, map[string]string{"error": containerapp.ErrBusy.Error()})
		return
	}
	defer r.mu.Unlock()
	if r.frozen {
		respond(w, 409, map[string]string{"error": containerapp.ErrBusy.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 25*time.Second)
	defer cancel()
	list, err := r.imageInventory(ctx, input.Protected)
	if err != nil {
		respond(w, 503, map[string]string{"error": "镜像引用检查失败"})
		return
	}
	if !deleting {
		respond(w, 200, list)
		return
	}
	for _, item := range list.Images {
		if item.ID != input.ID {
			continue
		}
		if !item.Removable {
			respond(w, 409, map[string]string{"error": containerapp.ErrImageProtected.Error()})
			return
		}
		if err = r.engine.call(ctx, "DELETE", "/images/"+url.PathEscape(input.ID)+"?force=false&noprune=true", nil, nil); err != nil {
			respond(w, 409, map[string]string{"error": containerapp.ErrImageProtected.Error()})
			return
		}
		if err = os.Remove(r.imageCatalogPath(input.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Error("remove image record", "error", err)
		}
		respond(w, 200, map[string]bool{"deleted": true})
		return
	}
	respond(w, 404, map[string]string{"error": containerapp.ErrNotFound.Error()})
}
