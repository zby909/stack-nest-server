package resumable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"stacknest/server/internal/project"
	"stacknest/server/internal/storage"
)

const ChunkSize int64 = 4 << 20
const MinChunkSize int64 = 64 << 10
const Lifetime = 24 * time.Hour

// MetadataJSONLimit 为合法元数据最多六倍的 JSON 转义及记录字段预留有界空间。
const MetadataJSONLimit int64 = 64 << 10

var (
	ErrNotFound = errors.New("upload not found or expired")
	ErrConflict = errors.New("upload offset or parameters changed")
	ErrLimit    = errors.New("too many unfinished uploads")
	ErrInvalid  = errors.New("invalid upload parameters")
	ErrChecksum = errors.New("upload checksum mismatch")
)

// Request 固定一次续传的文件和目标，客户端编号仅作幂等键，不作为权限凭据。
type Request struct {
	ReplaceDraftID string `json:"replaceDraftId,omitempty"`
	Preview        bool   `json:"preview,omitempty"`
	Notes          string `json:"notes,omitempty"`
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Slug           string `json:"slug"`
	ExpectedID     string `json:"expectedId"`
	FileName       string `json:"fileName"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
}

// Ticket 在数据卷持久化偏移与发布收据，服务重启后可以继续。
type Ticket struct {
	Request    Request   `json:"request"`
	OwnerID    string    `json:"ownerId"`
	TargetSlug string    `json:"targetSlug"`
	Offset     int64     `json:"offset"`
	ExpiresAt  time.Time `json:"expiresAt"`
	ResultID   string    `json:"resultId,omitempty"`
}

// Store 适用于单实例服务；短暂串行写入，文件只保存在静态目录之外。
type Store struct {
	dir     string
	guard   *storage.Guard
	maxSize int64
	mu      sync.Mutex
	now     func() time.Time
}

// New 使用应用数据卷，未完成文件不进入站点和数据库备份。
func New(guard *storage.Guard, maxSize int64) *Store {
	return &Store{dir: filepath.Join(guard.Dir, "uploads"), guard: guard, maxSize: min(maxSize, storage.MaxArchiveBytes), now: time.Now}
}

// validID 只允许规范 UUID，拒绝路径片段和平台特殊名称。
func validID(id string) bool {
	value, err := uuid.Parse(id)
	return err == nil && value.String() == id
}

// Validate 在写盘前限定归档、大小、摘要和元数据。
func (s *Store) Validate(input Request) error {
	if len(input.Notes) > 4096 || !validID(input.ID) || input.Size <= 0 || input.Size > s.maxSize {
		return ErrInvalid
	}
	hash, err := hex.DecodeString(input.SHA256)
	if err != nil || len(hash) != sha256.Size || strings.ToLower(input.SHA256) != input.SHA256 {
		return ErrInvalid
	}
	if input.FileName == "" || len(input.FileName) > 255 || strings.ContainsAny(input.FileName, "/\\\x00\r\n") {
		return ErrInvalid
	}
	if !project.SupportedUploadFile(input.FileName) {
		return ErrInvalid
	}
	if input.ReplaceDraftID != "" && (input.Kind != "update" || !input.Preview ||
		!strings.HasPrefix(input.ReplaceDraftID, "dep-") || !validID(strings.TrimPrefix(input.ReplaceDraftID, "dep-")) || input.ReplaceDraftID == "dep-"+input.ID) {
		return ErrInvalid
	}
	switch input.Kind {
	case "create":
		if strings.TrimSpace(input.Name) == "" || len(input.Name) > 256 || len(input.Description) > 4096 || input.ExpectedID != "" {
			return ErrInvalid
		}
		if input.Slug != "" && !project.ValidSlug(input.Slug) {
			return ErrInvalid
		}
	case "update":
		if !project.ValidSlug(input.Slug) || (input.ExpectedID == "" && !input.Preview) || len(input.ExpectedID) > 100 || input.Name != "" || input.Description != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Create 对重复请求返回原记录，限制每人两项、全局八项未完成上传。
func (s *Store) Create(owner string, input Request) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == "" || s.Validate(input) != nil {
		return Ticket{}, ErrInvalid
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Ticket{}, err
	}
	if existing, err := s.read(input.ID, owner); err == nil {
		if existing.Request != input {
			return Ticket{}, ErrConflict
		}
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Ticket{}, err
	}
	// 不允许用其他人的编号覆盖其记录。
	if _, err := os.Lstat(filepath.Join(s.dir, input.ID)); !errors.Is(err, os.ErrNotExist) {
		return Ticket{}, ErrConflict
	}
	active, own, count, err := s.sweep(owner)
	if err != nil {
		return Ticket{}, err
	}
	if active >= 8 || own >= 2 || count >= 128 {
		return Ticket{}, ErrLimit
	}
	if err := s.guard.Check(storage.UploadBudget); err != nil {
		return Ticket{}, err
	}
	target := input.Slug
	if target == "" {
		target = project.Slugify(input.Name)
		if target == "" {
			target = "project-" + input.ID
		}
	}
	ticket := Ticket{Request: input, OwnerID: owner, TargetSlug: target, ExpiresAt: s.now().UTC().Add(Lifetime)}
	dir := filepath.Join(s.dir, input.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return Ticket{}, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "archive"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Ticket{}, err
	}
	if err = file.Close(); err == nil {
		err = s.save(ticket)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return Ticket{}, err
	}
	return ticket, nil
}

// Get 仅向创建者返回已持久化进度，管理员也不能接管别人的上传。
func (s *Store) Get(id, owner string) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(id, owner)
}

// Append 仅提交完整分片，偏移不符时不写入，断线残片允许重传。
func (s *Store) Append(ctx context.Context, id, owner string, offset int64, data []byte) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, err := s.read(id, owner)
	if err != nil {
		return Ticket{}, err
	}
	if ticket.ResultID != "" || ticket.Offset != offset || offset >= ticket.Request.Size {
		return ticket, ErrConflict
	}
	remaining := ticket.Request.Size - offset
	if int64(len(data)) < min(MinChunkSize, remaining) || int64(len(data)) > min(ChunkSize, remaining) {
		return ticket, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return ticket, err
	}
	if err := s.guard.Check(storage.UploadBudget); err != nil {
		return ticket, err
	}
	file, err := os.OpenFile(filepath.Join(s.dir, id, "archive"), os.O_WRONLY, 0o600)
	if err != nil {
		return ticket, err
	}
	defer file.Close()
	if err := file.Truncate(offset); err != nil {
		return ticket, err
	}
	n, err := file.WriteAt(data, offset)
	if err != nil {
		return ticket, err
	}
	if n != len(data) {
		return ticket, io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return ticket, err
	}
	ticket.Offset += int64(n)
	if err := s.save(ticket); err != nil {
		return Ticket{}, err
	}
	return ticket, nil
}

// Publish 校验整个文件并持久化完成收据；回调须用上传编号保证发布幂等。
func (s *Store) Publish(ctx context.Context, id, owner string, publish func(Ticket, io.Reader) (string, error)) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, err := s.read(id, owner)
	if err != nil {
		return Ticket{}, err
	}
	if ticket.ResultID != "" {
		return ticket, nil
	}
	if ticket.Offset != ticket.Request.Size {
		return ticket, ErrConflict
	}
	file, err := os.Open(filepath.Join(s.dir, id, "archive"))
	if err != nil {
		return ticket, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := storage.Copy(ctx, nil, hash, io.LimitReader(file, ticket.Request.Size+1), 0); err != nil {
		return ticket, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != ticket.Request.SHA256 {
		return ticket, ErrChecksum
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ticket, err
	}
	resultID, err := publish(ticket, file)
	if err != nil {
		return ticket, err
	}
	ticket.ResultID = resultID
	if err := s.save(ticket); err != nil {
		return Ticket{}, err
	}
	_ = file.Close()
	_ = os.Remove(filepath.Join(s.dir, id, "archive"))
	return ticket, nil
}

// Delete 放弃上传仅移除该用户的临时文件，不会删除已经发布的项目。
func (s *Store) Delete(id, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.read(id, owner); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dir, id))
}

// read 校验记录与文件边界，未写入记录的尾部在下次分片提交时覆盖。
func (s *Store) read(id, owner string) (Ticket, error) {
	if !validID(id) {
		return Ticket{}, ErrNotFound
	}
	dir := filepath.Join(s.dir, id)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return Ticket{}, ErrNotFound
	}
	if err != nil {
		return Ticket{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Ticket{}, ErrInvalid
	}
	file, err := os.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		return Ticket{}, err
	}
	var ticket Ticket
	decodeErr := json.NewDecoder(io.LimitReader(file, MetadataJSONLimit)).Decode(&ticket)
	closeErr := file.Close()
	if decodeErr != nil {
		return Ticket{}, decodeErr
	}
	if closeErr != nil {
		return Ticket{}, closeErr
	}
	if ticket.OwnerID != owner && owner != "" {
		return Ticket{}, ErrNotFound
	}
	if ticket.Request.ID != id || s.Validate(ticket.Request) != nil || !project.ValidSlug(ticket.TargetSlug) || ticket.Offset < 0 || ticket.Offset > ticket.Request.Size {
		return Ticket{}, ErrInvalid
	}
	if !s.now().Before(ticket.ExpiresAt) {
		if err := os.RemoveAll(dir); err != nil {
			return Ticket{}, err
		}
		return Ticket{}, ErrNotFound
	}
	if ticket.ResultID == "" {
		info, err := os.Lstat(filepath.Join(dir, "archive"))
		if err != nil {
			return Ticket{}, err
		}
		if !info.Mode().IsRegular() || info.Size() < ticket.Offset || info.Size() > ticket.Request.Size {
			return Ticket{}, ErrInvalid
		}
	}
	return ticket, nil
}

// save 先同步临时记录再替换，避免重启后报告尚未写稳的分片进度。
func (s *Store) save(ticket Ticket) error {
	dir := filepath.Join(s.dir, ticket.Request.ID)
	file, err := os.OpenFile(filepath.Join(dir, "state.tmp"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(file).Encode(ticket)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(filepath.Join(dir, "state.tmp"), filepath.Join(dir, "state.json"))
}

// Cleanup 定期回收过期记录；只扫描本模块创建的 UUID 目录。
func (s *Store) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, _, err := s.sweep("")
	return err
}

// sweep 在锁内删除过期上传并统计配额，避免多请求同时绕过数量限制。
func (s *Store) sweep(owner string) (active, own, count int, err error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, 0, nil
	}
	if err != nil {
		return 0, 0, 0, err
	}
	for _, entry := range entries {
		if !validID(entry.Name()) {
			continue
		}
		ticket, readErr := s.read(entry.Name(), "")
		if errors.Is(readErr, ErrNotFound) {
			continue
		}
		if readErr != nil {
			// 初次保存前崩溃可能只留下目录；计入配额，到期后回收，不阻断其他上传。
			info, statErr := entry.Info()
			if statErr != nil {
				return 0, 0, 0, statErr
			}
			if s.now().After(info.ModTime().Add(Lifetime)) {
				if err := os.RemoveAll(filepath.Join(s.dir, entry.Name())); err != nil {
					return 0, 0, 0, err
				}
			} else {
				count++
				active++
			}
			continue
		}
		count++
		if ticket.ResultID == "" {
			active++
			if ticket.OwnerID == owner {
				own++
			}
		}
	}
	return active, own, count, nil
}
