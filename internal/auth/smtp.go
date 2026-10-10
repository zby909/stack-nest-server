package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTPConfig 是使用隐式 TLS 发送验证码和邀请的配置。
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
}

// SMTPMailer 使用 SMTP over TLS 发送登录验证码和邀请。
type SMTPMailer struct{ config SMTPConfig }

// NewSMTPMailer 检查 SMTP 连接参数。
func NewSMTPMailer(config SMTPConfig) (*SMTPMailer, error) {
	if config.Host == "" || config.Port < 1 || config.Port > 65535 || config.Username == "" || config.Password == "" {
		return nil, fmt.Errorf("SMTP host, port, username and password are required")
	}
	username, err := NormalizeEmail(config.Username)
	if err != nil {
		return nil, fmt.Errorf("invalid SMTP sender: %w", err)
	}
	config.Username = username
	return &SMTPMailer{config: config}, nil
}

// SendCode 发送带有效期说明的验证码邮件。
func (m *SMTPMailer) SendCode(ctx context.Context, email, code string) error {
	body := "StackNest login code: " + code + "\r\n\r\nThis code expires in 10 minutes. If you did not request it, ignore this email.\r\n"
	return m.send(ctx, email, "StackNest login code", body)
}

// SendInvitation 发送普通用户邀请，仅包含固定登录地址与登录方式。
func (m *SMTPMailer) SendInvitation(ctx context.Context, email, loginURL string) error {
	body := invitationBody(email, loginURL)
	return m.send(ctx, email, "Invitation to StackNest", body)
}

// invitationBody 生成邀请正文，收件人仍需自行请求一次性登录验证码。
func invitationBody(email, loginURL string) string {
	return "你已被管理员添加为 StackNest 普通用户。\r\n\r\n" +
		"登录地址：" + loginURL + "\r\n" +
		"登录邮箱：" + email + "\r\n\r\n" +
		"打开登录页面，输入该邮箱并获取邮件一次性验证码即可登录，无需设置密码。\r\n" +
		"登录后可上传、更新和删除自己的静态项目。\r\n\r\n" +
		"如网站暂时无法访问，请联系邀请你的管理员。若未预期收到本邮件，可以忽略。\r\n"
}

// send 复用隐式 TLS 的 SMTP 连接，并将整次发信限制在 20 秒内。
func (m *SMTPMailer) send(ctx context.Context, email, subject, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	email, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	address := net.JoinHostPort(m.config.Host, strconv.Itoa(m.config.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect SMTP: %w", err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(20 * time.Second))
	}
	tlsConnection := tls.Client(connection, &tls.Config{ServerName: m.config.Host, MinVersion: tls.VersionTLS12})
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("SMTP TLS handshake: %w", err)
	}
	client, err := smtp.NewClient(tlsConnection, m.config.Host)
	if err != nil {
		return fmt.Errorf("start SMTP client: %w", err)
	}
	defer client.Close()
	if err := client.Auth(smtp.PlainAuth("", m.config.Username, m.config.Password, m.config.Host)); err != nil {
		return fmt.Errorf("authenticate SMTP: %w", err)
	}
	if err := client.Mail(m.config.Username); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(email); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("write SMTP data: %w", err)
	}
	message := strings.Join([]string{
		"From: " + m.config.Username,
		"To: " + email,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body,
	}, "\r\n")
	if _, err := io.WriteString(writer, message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("send SMTP message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("complete SMTP message: %w", err)
	}
	return client.Quit()
}
