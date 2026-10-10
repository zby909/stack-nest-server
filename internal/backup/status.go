package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Attempt 只包含安全的时间与阶段名，不包含命令、凭据或内部路径。
type Attempt struct {
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Stage      string     `json:"stage"`
}

// Success 记录最近一个完整备份的时间和两种备份文件的体积。
type Success struct {
	CreatedAt     time.Time `json:"createdAt"`
	DatabaseBytes int64     `json:"databaseBytes"`
	FilesBytes    int64     `json:"filesBytes"`
	TotalBytes    int64     `json:"totalBytes"`
}

// Status 是管理员可见的备份摘要，不直接返回原始文件。
type Status struct {
	State         string   `json:"state"`
	LastAttempt   *Attempt `json:"lastAttempt,omitempty"`
	LastSuccess   *Success `json:"lastSuccess,omitempty"`
	RetainedCount int      `json:"retainedCount"`
	RetainedBytes int64    `json:"retainedBytes"`
	Overdue       bool     `json:"overdue"`
}

// Read 有界读取原子发布的状态，区分未配置、尚无记录与中断的备份。
func Read(path string, now time.Time) (Status, error) {
	if path == "" {
		return Status{State: "not_configured"}, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{State: "never"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return Status{}, fmt.Errorf("backup status is not a small regular file")
	}
	var record struct {
		Version int `json:"version"`
		Status
	}
	decoder := json.NewDecoder(io.LimitReader(file, (64<<10)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Status{}, fmt.Errorf("decode backup status: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Status{}, fmt.Errorf("backup status has trailing data")
	}
	if record.Version != 1 || !valid(record.Status) {
		return Status{}, fmt.Errorf("invalid backup status")
	}
	value := record.Status
	if value.State == "running" && now.Sub(value.LastAttempt.StartedAt) > 45*time.Minute {
		value.State = "interrupted"
	}
	value.Overdue = value.LastSuccess != nil && now.Sub(value.LastSuccess.CreatedAt) > 48*time.Hour
	return value, nil
}

// valid 校验摘要字段，拒绝损坏状态或意外加入的内部信息。
func valid(value Status) bool {
	if value.State != "running" && value.State != "succeeded" && value.State != "failed" {
		return false
	}
	if value.LastAttempt == nil || value.LastAttempt.StartedAt.IsZero() || value.RetainedCount < 0 || value.RetainedBytes < 0 {
		return false
	}
	stages := map[string]bool{"initialize": true, "stop": true, "apps-freeze": true, "apps-restore": true, "database": true, "files": true, "verify": true, "publish": true, "prune": true, "restart": true, "completed": true}
	if !stages[value.LastAttempt.Stage] {
		return false
	}
	finished := value.LastAttempt.FinishedAt
	if value.State == "running" && finished != nil || value.State != "running" && (finished == nil || finished.Before(value.LastAttempt.StartedAt)) {
		return false
	}
	if value.LastSuccess != nil {
		last := value.LastSuccess
		if last.CreatedAt.IsZero() || last.DatabaseBytes <= 0 || last.FilesBytes <= 0 || last.TotalBytes <= 0 || last.DatabaseBytes > last.TotalBytes || last.FilesBytes != last.TotalBytes-last.DatabaseBytes || value.RetainedCount == 0 || value.RetainedBytes < last.TotalBytes {
			return false
		}
	} else if value.RetainedCount != 0 || value.RetainedBytes != 0 {
		return false
	}
	return value.State != "succeeded" || value.LastSuccess != nil
}
