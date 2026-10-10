package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// NormalizeInvitationURL 校验管理员配置的 HTTPS 登录地址，禁止凭据和查询令牌进入邀请。
func NormalizeInvitationURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.ContainsAny(raw, "\r\n\t ") {
		return "", errors.New("a configured HTTPS login URL without credentials or query parameters is required")
	}
	return parsed.String(), nil
}

// SendInvitation 向新增邮箱发送登录说明，不生成验证码或创建会话。
func (s *Service) SendInvitation(ctx context.Context, rawEmail, rawURL string) error {
	email, err := NormalizeEmail(rawEmail)
	if err != nil {
		return err
	}
	loginURL, err := NormalizeInvitationURL(rawURL)
	if err != nil {
		return err
	}
	return s.mailer.SendInvitation(ctx, email, loginURL)
}
