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

// SMTPConfig 是使用隐式 TLS 发送验证码的配置。
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
}

// SMTPMailer 使用 SMTP over TLS 发送登录验证码。
type SMTPMailer struct{ config SMTPConfig }

// NewSMTPMailer 检查 SMTP 连接参数。
func NewSMTPMailer(config SMTPConfig) (*SMTPMailer, error) {
	if config.Host == "" || config.Port < 1 || config.Port > 65535 || config.Username == "" || config.Password == "" {
		return nil, fmt.Errorf("SMTP host, port, username and password are required")
	}
	return &SMTPMailer{config: config}, nil
}

// SendCode 通过 TLS 验证过的 SMTP 连接发送纯文本邮件。
func (m *SMTPMailer) SendCode(ctx context.Context, email, code string) error {
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
	body := "StackNest login code: " + code + "\r\n\r\nThis code expires in 10 minutes. If you did not request it, ignore this email.\r\n"
	message := strings.Join([]string{
		"From: " + m.config.Username,
		"To: " + email,
		"Subject: StackNest login code",
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
