package activity

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const segmentLimit = 1 << 20

// Event 保存可展示的操作结果；不记录请求体、验证码、Cookie 或授权码。
type Event struct {
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"createdAt"`
	Action         string    `json:"action"`
	Actor          string    `json:"actor"`
	ActorID        string    `json:"actorId,omitempty"`
	ProjectOwnerID string    `json:"projectOwnerId,omitempty"`
	Slug           string    `json:"slug,omitempty"`
	FileName       string    `json:"fileName,omitempty"`
	ArchiveBytes   int64     `json:"archiveBytes"`
	ExpandedBytes  int64     `json:"expandedBytes"`
	FileCount      int       `json:"fileCount"`
	Status         string    `json:"status"`
	Message        string    `json:"message"`
	DurationMS     int64     `json:"durationMs"`
}

// Store 在私有数据卷内维护最多三段 JSON Lines 操作日志，面向单 API 实例。
type Store struct {
	mu   sync.Mutex
	path string
}

// New 创建日志目录，沿用站点数据卷的持久化与备份边界。
func New(dataDir string) (*Store, error) {
	directory := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	return &Store{path: filepath.Join(directory, "activity.jsonl")}, nil
}

// Append 有界追加并落盘；轮转限制日志自身的磁盘使用。
func (s *Store) Append(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	event.FileName = clip(event.FileName, 255)
	event.Message = clip(event.Message, 512)
	event.Slug = clip(event.Slug, 52)
	event.Actor = clip(event.Actor, 254)
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if info, err := os.Stat(s.path); err == nil && info.Size()+int64(len(line))+1 > segmentLimit {
		if err := s.rotate(); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := file.ReadAt(last, info.Size()-1); err != nil {
			return err
		}
		// 保留崩溃留下的不完整行，并避免下一条日志与它拼接。
		if last[0] != '\n' {
			if _, err := file.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

// List 读取最近一百条完整记录，跳过进程中断留下的不完整尾行。
func (s *Store) List() ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := make([]Event, 0, 100)
	for _, suffix := range []string{".2", ".1", ""} {
		file, err := os.Open(s.path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			var event Event
			if json.Unmarshal(scanner.Bytes(), &event) == nil && event.ID != "" {
				events = append(events, event)
				if len(events) > 100 {
					events = events[1:]
				}
			}
		}
		scanErr := scanner.Err()
		file.Close()
		if scanErr != nil {
			return nil, fmt.Errorf("read activity log: %w", scanErr)
		}
	}
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	return events, nil
}

// rotate 只保留当前文件及两个历史片段。
func (s *Store) rotate() error {
	if err := os.Remove(s.path + ".2"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(s.path+".1", s.path+".2"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(s.path, s.path+".1")
}

// clip 限制用户可控文本长度，并保持 Unicode 完整。
func clip(value string, limit int) string {
	text := []rune(value)
	if len(text) > limit {
		return string(text[:limit])
	}
	return value
}
