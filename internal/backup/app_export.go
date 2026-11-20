package backup

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var exportDirectory = regexp.MustCompile(`^app-export-[a-z0-9_]{8}$`)

// AppExport 仅向已授权的应用成员公开该应用的数据快照信息。
type AppExport struct {
	State     string     `json:"state"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	Bytes     int64      `json:"bytes,omitempty"`
	Snapshot  string     `json:"snapshot,omitempty"`
	path      string
}

// LookupAppExport 只承认最新成功备份的索引，不会把旧的文件展示为最新数据。
func LookupAppExport(statusPath, id string) (AppExport, error) {
	status, err := Read(statusPath, time.Now().UTC())
	if err != nil || status.LastSuccess == nil {
		return AppExport{State: "no_backup"}, err
	}
	value := AppExport{State: "preparing", CreatedAt: &status.LastSuccess.CreatedAt}
	dir := filepath.Dir(statusPath)
	f, err := os.Open(filepath.Join(dir, "app-exports.json"))
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	defer f.Close()
	var index struct {
		Directory string
		CreatedAt time.Time
		Apps      map[string]struct {
			State string
			Bytes int64
		}
	}
	if err = json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&index); err != nil {
		return value, err
	}
	if !index.CreatedAt.Equal(*value.CreatedAt) {
		return value, nil
	}
	if !exportDirectory.MatchString(index.Directory) || filepath.Base(id) != id {
		return value, errors.New("invalid export index")
	}
	entry, ok := index.Apps[id]
	if !ok {
		value.State = "no_data"
		return value, nil
	}
	if entry.State == "unsupported" {
		value.State = "unsupported"
		return value, nil
	}
	if entry.State != "ready" || entry.Bytes <= 0 {
		return value, errors.New("invalid export record")
	}
	value.State, value.Bytes = "ready", entry.Bytes
	value.Snapshot = index.Directory
	value.path = filepath.Join(dir, index.Directory, id+".tar.gz")
	return value, nil
}

// Open 打开已经生成的归档并检查大小，轮换时打开的文件仍保持有效。
func (e AppExport) Open() (*os.File, error) {
	f, err := os.Open(e.path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != e.Bytes {
		f.Close()
		return nil, errors.New("invalid export file")
	}
	return f, nil
}
