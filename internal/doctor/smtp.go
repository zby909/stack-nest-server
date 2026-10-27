package doctor

import (
	"context"
	"crypto/tls"
	"net"
	"net/smtp"
	"stacknest/server/internal/config"
	"strconv"
	"time"
)

// probeSMTP 只进行 TLS、SMTP 认证和 QUIT，绝不发出 MAIL、RCPT、DATA。
func probeSMTP(ctx context.Context, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{ServerName: cfg.SMTPHost, MinVersion: tls.VersionTLS12}}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort)))
	if err != nil {
		return err
	}
	defer conn.Close()
	// stopClose 在浏览器取消诊断后立即打断 SMTP 读写。
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		return err
	}
	defer client.Close()
	if err = client.Auth(smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)); err != nil {
		return err
	}
	return client.Quit()
}
