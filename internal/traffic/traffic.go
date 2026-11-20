// Package traffic 保存 Nginx 源站的七日计数，不记录 URL、IP 或用户身份。
package traffic

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type Target struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
}
type Targets map[string]Target

type Counts struct {
	Requests     int64 `json:"requests"`
	Pages        int64 `json:"pages"`
	ClientErrors int64 `json:"clientErrors"`
	ServerErrors int64 `json:"serverErrors"`
}
type Day struct {
	Date string `json:"date"`
	Counts
}
type Summary struct {
	Available bool      `json:"available"`
	Since     time.Time `json:"since,omitzero"`
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
	Days      []Day     `json:"days"`
}
type snapshot struct {
	Since     time.Time
	UpdatedAt time.Time
	Days      map[string]map[string]Counts
}

// atomicFile 替换整个小文件，使独立的 API 和 Runner 总能读到完整一代数据。
func atomicFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".traffic-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(0644)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

type Registry struct {
	Dir        string
	BaseDomain string
	Load       func(context.Context) (Targets, error)
	mu         sync.Mutex
}

// Refresh 发布域名与不可变资源 ID 的对应关系。相同 slug 重新创建时从新项目开始计数。
func (reg *Registry) Refresh(ctx context.Context) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	routes, err := reg.Load(ctx)
	if err != nil {
		return err
	}
	hosts := Targets{}
	for label, target := range routes {
		hosts[label+".localhost"] = target
		if reg.BaseDomain != "" {
			hosts[label+"."+strings.ToLower(reg.BaseDomain)] = target
		}
	}
	body, err := json.Marshal(hosts)
	if err != nil {
		return err
	}
	path := filepath.Join(reg.Dir, "traffic-routes.json")
	prior, err := os.ReadFile(path)
	if err == nil && slices.Equal(prior, body) {
		return nil
	}
	return atomicFile(path, body)
}

// Read 只返回指定资源的七个 UTC 自然日，覆盖尚无请求、刚安装和采集中断的状态。
func Read(dir, id string, now time.Time) (Summary, error) {
	now = now.UTC()
	view := Summary{Days: make([]Day, 0, 7)}
	var state snapshot
	body, err := os.ReadFile(filepath.Join(dir, "traffic-counts.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return view, err
	}
	if err == nil {
		if err = json.Unmarshal(body, &state); err != nil {
			return view, err
		}
		view.Available = now.Sub(state.UpdatedAt) < 2*time.Minute
		view.Since, view.UpdatedAt = state.Since, state.UpdatedAt
	}
	for i := -6; i <= 0; i++ {
		date := now.AddDate(0, 0, i).Format(time.DateOnly)
		view.Days = append(view.Days, Day{Date: date, Counts: state.Days[date][id]})
	}
	return view, nil
}
