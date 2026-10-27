// Package setup 生成新安装的 Compose 配置，不读取或修改已有实例的密钥。
package setup

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"stacknest/server/internal/auth"
)

// Settings 只包含首次安装需要用户提供的选项。
type Settings struct {
	BaseDomain, AdminDomain, AdminEmail string
	SMTPHost, SMTPUser, SMTPPassword    string
	SMTPPort                            int
}

var hostnameLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// validHostname 限制域名为可直接用于 DNS 和 Nginx 配置的 ASCII 主机名。
func validHostname(value string) bool {
	if len(value) > 253 || value == "" {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !hostnameLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// Validate 与正式邮件服务共用邮箱校验，拒绝可破坏配置格式的控制字符。
func (s Settings) Validate() error {
	if !validHostname(s.BaseDomain) || !strings.Contains(s.BaseDomain, ".") || net.ParseIP(s.BaseDomain) != nil {
		return errors.New("项目基础域名无效，请填写 example.com 形式的域名")
	}
	if !validHostname(s.AdminDomain) || !strings.HasSuffix(s.AdminDomain, "."+s.BaseDomain) {
		return errors.New("管理域名须为项目基础域名下的子域名")
	}
	label := strings.TrimSuffix(s.AdminDomain, "."+s.BaseDomain)
	if strings.Contains(label, ".") || strings.HasPrefix(label, "stack-nest-") {
		return errors.New("管理域名须为一级子域名，不能占用 stack-nest-* 项目域名")
	}
	if _, err := auth.NormalizeEmail(s.AdminEmail); err != nil {
		return errors.New("管理员邮箱格式无效")
	}
	if _, err := auth.NormalizeEmail(s.SMTPUser); err != nil {
		return errors.New("SMTP 发件邮箱格式无效")
	}
	if !validHostname(s.SMTPHost) || s.SMTPPort < 1 || s.SMTPPort > 65535 {
		return errors.New("SMTP 主机或端口无效")
	}
	if strings.TrimSpace(s.SMTPPassword) == "" || len(s.SMTPPassword) > 4096 || !utf8.ValidString(s.SMTPPassword) || strings.ContainsFunc(s.SMTPPassword, unicode.IsControl) {
		return errors.New("SMTP 授权码不能为空、超过 4096 字节或包含控制字符")
	}
	return nil
}

// Render 只替换模板中的必要配置，完整保留 Compose 入口、镜像摘要和版本信息。
func Render(template string, s Settings) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	password, secret := make([]byte, 32), make([]byte, 48)
	if _, err := rand.Read(password); err != nil {
		return nil, err
	}
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	databasePassword := hex.EncodeToString(password)
	values := map[string]string{
		"STACKNEST_BASE_DOMAIN": s.BaseDomain, "STACKNEST_ADMIN_DOMAIN": s.AdminDomain,
		"STACKNEST_AUTH_ADMIN_EMAIL":  s.AdminEmail,
		"STACKNEST_POSTGRES_PASSWORD": databasePassword,
		"STACKNEST_DATABASE_URL":      "postgres://stacknest:" + databasePassword + "@postgres:5432/stacknest?sslmode=disable",
		"STACKNEST_AUTH_SECRET":       base64.StdEncoding.EncodeToString(secret),
		"STACKNEST_SMTP_HOST":         s.SMTPHost, "STACKNEST_SMTP_PORT": strconv.Itoa(s.SMTPPort),
		"STACKNEST_SMTP_USER": s.SMTPUser, "STACKNEST_SMTP_PASSWORD": s.SMTPPassword,
	}
	seen := make(map[string]bool, len(values))
	lines := strings.Split(strings.ReplaceAll(template, "\r\n", "\n"), "\n")
	for index, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		value, wanted := values[key]
		if !ok || !wanted {
			continue
		}
		if seen[key] {
			return nil, fmt.Errorf("模板重复配置项：%s", key)
		}
		seen[key] = true
		// Compose 的双引号保护空格、引号和反斜杠，$$ 阻止密码被当成变量插值。
		lines[index] = key + "=" + strconv.Quote(strings.ReplaceAll(value, "$", "$$"))
	}
	if len(seen) != len(values) {
		return nil, errors.New("部署模板缺少必要配置项，请使用完整发布包")
	}
	return []byte(strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"), nil
}

// Create 独占创建权限为 0600 的配置；即使发生竞争或符号链接也不会覆盖旧文件。
func Create(dir string, content []byte) (err error) {
	path := filepath.Join(dir, ".env")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("无法创建 .env（不会覆盖已有文件）：%w", err)
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if _, err = file.Write(content); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	err = file.Close()
	return err
}
