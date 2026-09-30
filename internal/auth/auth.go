package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/mail"
	"strings"
	"time"
)

const (
	RoleAdmin  = "admin"
	RoleUser   = "user"
	CodeTTL    = 10 * time.Minute
	SessionTTL = 7 * 24 * time.Hour
)

var (
	ErrNotFound     = errors.New("user or session not found")
	ErrExists       = errors.New("email already exists")
	ErrRateLimited  = errors.New("code request is rate limited")
	ErrInvalidCode  = errors.New("invalid or expired code")
	ErrInvalidEmail = errors.New("invalid email")
	ErrCannotDelete = errors.New("cannot delete this user")
)

// User 是管理端允许登录的邮箱及其权限。
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store 负责验证码、会话和允许登录的邮箱的持久化。
type Store interface {
	BootstrapUsers(ctx context.Context, adminEmail, initialUserEmail string) error
	FindUserByEmail(ctx context.Context, email string) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	AddUser(ctx context.Context, email string) (User, error)
	DeleteUser(ctx context.Context, id string) error
	SaveChallenge(ctx context.Context, userID, codeHash string, sentAt, expiresAt time.Time) error
	DiscardChallenge(ctx context.Context, userID, codeHash string) error
	ExchangeChallenge(ctx context.Context, userID, codeHash, tokenHash string, now, sessionExpiresAt time.Time) error
	FindSession(ctx context.Context, tokenHash string, now time.Time) (User, error)
	DeleteSession(ctx context.Context, tokenHash string) error
}

// Mailer 发送一次性登录验证码。
type Mailer interface {
	SendCode(ctx context.Context, email, code string) error
}

// Service 实现一次性验证码和服务端会话规则。
type Service struct {
	store  Store
	mailer Mailer
	secret []byte
	now    func() time.Time
}

// NewService 创建认证服务并检查密钥长度。
func NewService(store Store, mailer Mailer, secret string) (*Service, error) {
	if store == nil || mailer == nil || len(secret) < 32 {
		return nil, errors.New("auth store, mailer and a 32-byte secret are required")
	}
	return &Service{store: store, mailer: mailer, secret: []byte(secret), now: time.Now}, nil
}

// NormalizeEmail 规范化并检查一个完整邮箱地址。
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if len(email) == 0 || len(email) > 254 || strings.ContainsAny(email, "\r\n\t ") {
		return "", ErrInvalidEmail
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || strings.Count(email, "@") != 1 {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// BootstrapUsers 只在用户表为空时创建首个管理员和普通用户。
func (s *Service) BootstrapUsers(ctx context.Context, admin, initialUser string) error {
	adminEmail, err := NormalizeEmail(admin)
	if err != nil {
		return fmt.Errorf("invalid admin email: %w", err)
	}
	if initialUser != "" {
		initialUser, err = NormalizeEmail(initialUser)
		if err != nil || initialUser == adminEmail {
			return errors.New("invalid initial user email")
		}
	}
	return s.store.BootstrapUsers(ctx, adminEmail, initialUser)
}

// RequestCode 为已获准的邮箱生成一次性验证码并发送；未知邮箱静默返回。
func (s *Service) RequestCode(ctx context.Context, rawEmail string) error {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return err
	}
	user, err := s.store.FindUserByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	value, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return err
	}
	code := fmt.Sprintf("%08d", value)
	hash := s.codeHash(user.ID, code)
	now := s.now().UTC()
	if err := s.store.SaveChallenge(ctx, user.ID, hash, now, now.Add(CodeTTL)); err != nil {
		if errors.Is(err, ErrRateLimited) {
			return nil
		}
		return err
	}
	if err := s.mailer.SendCode(ctx, email, code); err != nil {
		_ = s.store.DiscardChallenge(ctx, user.ID, hash)
		return err
	}
	return nil
}

// VerifyCode 消费验证码并创建新的可撤销会话。
func (s *Service) VerifyCode(ctx context.Context, rawEmail, code string) (User, string, error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil || len(code) != 8 {
		return User{}, "", ErrInvalidCode
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return User{}, "", ErrInvalidCode
		}
	}
	user, err := s.store.FindUserByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		return User{}, "", ErrInvalidCode
	}
	if err != nil {
		return User{}, "", err
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return User{}, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	now := s.now().UTC()
	if err := s.store.ExchangeChallenge(ctx, user.ID, s.codeHash(user.ID, code), TokenHash(token), now, now.Add(SessionTTL)); err != nil {
		return User{}, "", err
	}
	return user, token, nil
}

// CurrentUser 根据随机会话令牌查询当前用户。
func (s *Service) CurrentUser(ctx context.Context, token string) (User, error) {
	if len(token) != 43 {
		return User{}, ErrNotFound
	}
	return s.store.FindSession(ctx, TokenHash(token), s.now().UTC())
}

// Logout 删除当前令牌对应的服务端会话。
func (s *Service) Logout(ctx context.Context, token string) error {
	if len(token) != 43 {
		return nil
	}
	return s.store.DeleteSession(ctx, TokenHash(token))
}

// ListUsers 返回全部获准登录的邮箱。
func (s *Service) ListUsers(ctx context.Context) ([]User, error) { return s.store.ListUsers(ctx) }

// AddUser 将普通用户邮箱加入登录名单。
func (s *Service) AddUser(ctx context.Context, rawEmail string) (User, error) {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return User{}, err
	}
	return s.store.AddUser(ctx, email)
}

// DeleteUser 移除普通用户，并同时撤销该用户全部会话。
func (s *Service) DeleteUser(ctx context.Context, id string) error {
	return s.store.DeleteUser(ctx, id)
}

// TokenHash 只保留随机会话令牌的摘要。
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// codeHash 使用服务端密钥保护低熵的数字验证码。
func (s *Service) codeHash(userID, code string) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(userID + ":" + code))
	return hex.EncodeToString(mac.Sum(nil))
}
