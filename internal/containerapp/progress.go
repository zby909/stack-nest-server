package containerapp

import "time"

// Progress 是当前操作代次的安全快照，不包含 Docker 原文或环境变量。
type Progress struct {
	CanCancel       bool      `json:"canCancel"`
	Generation      int64     `json:"generation"`
	Stage           string    `json:"stage"`
	FailedStage     string    `json:"failedStage,omitempty"`
	DownloadedBytes int64     `json:"downloadedBytes,omitempty"`
	TotalBytes      int64     `json:"totalBytes,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt"`
}
