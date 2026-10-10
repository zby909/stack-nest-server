package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type invitationMailer struct {
	email string
	url   string
	err   error
	calls int
}

// SendCode 禁止邀请测试意外触发验证码发送。
func (m *invitationMailer) SendCode(context.Context, string, string) error {
	panic("an invitation must not send a login code")
}

// SendInvitation 记录邀请收件人与地址，并允许模拟 SMTP 失败。
func (m *invitationMailer) SendInvitation(_ context.Context, email, loginURL string) error {
	m.calls++
	m.email, m.url = email, loginURL
	return m.err
}

// TestInvitationValidation 验证邀请只使用完整邮箱和固定 HTTPS 地址，拒绝邮件头与令牌注入。
func TestInvitationValidation(t *testing.T) {
	for _, raw := range []string{
		"", "http://localhost:18080/login", "//example.com/login", "https:///login",
		"https://user:secret@example.com/login", "https://example.com/login?token=secret",
		"https://example.com/login?", "https://example.com/login#secret",
		"https://example.com/login\r\nBcc: victim@example.com", "https://example.com/a b",
	} {
		if _, err := NormalizeInvitationURL(raw); err == nil {
			t.Errorf("unsafe invitation URL accepted: %q", raw)
		}
	}
	mailer := &invitationMailer{}
	s := &Service{mailer: mailer}
	if err := s.SendInvitation(context.Background(), " User@Example.com ", " https://stack.example.com/login "); err != nil {
		t.Fatal(err)
	}
	if mailer.calls != 1 || mailer.email != "user@example.com" || mailer.url != "https://stack.example.com/login" {
		t.Fatalf("unexpected invitation destination: %+v", mailer)
	}
	if err := s.SendInvitation(context.Background(), "user@example.com\r\nBcc: victim@example.com", mailer.url); err == nil {
		t.Fatal("invalid recipient accepted")
	}
	if err := s.SendInvitation(context.Background(), "user@example.com", "https://example.com/?token=secret"); err == nil {
		t.Fatal("unsafe login URL accepted")
	}
	if mailer.calls != 1 {
		t.Fatal("invalid invitation contacted mailer")
	}
	mailer.err = errors.New("SMTP unavailable")
	if err := s.SendInvitation(context.Background(), "user@example.com", mailer.url); !errors.Is(err, mailer.err) {
		t.Fatalf("SMTP failure was not returned: %v", err)
	}
}

// TestInvitationInstructions 确认邀请说明验证码登录方式，不包含自动登录凭据。
func TestInvitationInstructions(t *testing.T) {
	body := invitationBody("user@example.com", "https://stack.example.com/login")
	for _, text := range []string{"https://stack.example.com/login", "user@example.com", "普通用户", "获取邮件一次性验证码", "自己的静态项目"} {
		if !strings.Contains(body, text) {
			t.Errorf("invitation missing %q", text)
		}
	}
	for _, text := range []string{"token=", "code=", "stacknest_session"} {
		if strings.Contains(body, text) {
			t.Errorf("invitation contains a login credential: %q", text)
		}
	}
}

// TestSMTPRejectsHeaderInjection 确认 SMTP 发信前拒绝恶意发件人与收件人。
func TestSMTPRejectsHeaderInjection(t *testing.T) {
	config := SMTPConfig{Host: "smtp.example.com", Port: 465, Username: "sender@example.com\r\nBcc: victim@example.com", Password: "test-only"}
	if _, err := NewSMTPMailer(config); err == nil {
		t.Fatal("invalid SMTP sender accepted")
	}
	config.Username = "sender@example.com"
	mailer, err := NewSMTPMailer(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := mailer.SendInvitation(context.Background(), "recipient@example.com\r\nBcc: victim@example.com", "https://stack.example.com/login"); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("invalid recipient not rejected before connection: %v", err)
	}
}
