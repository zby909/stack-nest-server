// Package deploytoken 定义仅用于单个静态项目自动部署的凭据。
package deploytoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"stacknest/server/internal/auth"
)

var (
	ErrInvalid = errors.New("invalid deployment token")
	ErrLimit   = errors.New("deployment token limit reached")
)

const MaxTokens = 10

// Token 的公开字段不包含原始凭据、摘要或内部授权信息。
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CanPublish bool       `json:"canPublish"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedBy  string     `json:"createdBy"`
	ProjectID  string     `json:"-"`
	OwnerID    string     `json:"-"`
	CreatorID  string     `json:"-"`
	Hash       string     `json:"-"`
}

// Grant 来自数据库当前状态；不缓存被删除用户或已转移项目的权限。
type Grant struct {
	Token
	Slug string
	User auth.User
}

// Store 同时验证项目归属与用户权限，HTTP 层不能自行扩大 Token 的范围。
type Store interface {
	CreateDeployToken(context.Context, Token, auth.User) error
	ListDeployTokens(context.Context, string, auth.User) ([]Token, error)
	DeleteDeployToken(context.Context, string, string, auth.User) error
	AuthenticateDeployToken(context.Context, string) (Grant, error)
}

// New 生成 256 位随机凭据，调用方仅在创建响应中展示一次原文。
func New(projectID, name string, days int, publish bool) (Token, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 || strings.IndexFunc(name, unicode.IsControl) >= 0 || days < 1 || days > 365 {
		return Token{}, "", ErrInvalid
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Token{}, "", err
	}
	plain := "snd_" + base64.RawURLEncoding.EncodeToString(secret)
	hash, _ := Hash(plain)
	now := time.Now().UTC()
	return Token{ID: uuid.NewString(), ProjectID: projectID, Name: name, CanPublish: publish, CreatedAt: now, ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour), Hash: hash}, plain, nil
}

// Hash 只接受规范随机 Token，避免将任意输入传入查询或日志。
func Hash(plain string) (string, error) {
	if !strings.HasPrefix(plain, "snd_") || len(plain) != 47 {
		return "", ErrInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(plain[4:])
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != plain[4:] {
		return "", ErrInvalid
	}
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:]), nil
}
