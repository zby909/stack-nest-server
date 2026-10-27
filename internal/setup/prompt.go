package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"stacknest/server/internal/auth"
)

// Run 在终端收集非敏感选项，通过专用回调读取不回显的密码。
func Run(dir string, in io.Reader, out io.Writer, password func() (string, error)) error {
	if _, err := os.Lstat(filepath.Join(dir, ".env")); err == nil {
		return errors.New("已存在 .env；首次配置向导不会覆盖它，升级请保留原密码和密钥")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	template, err := os.ReadFile(filepath.Join(dir, ".env.compose.example"))
	if err != nil {
		return errors.New("找不到 .env.compose.example，请在解压后的 server 目录运行")
	}
	fmt.Fprintln(out, "StackNest 首次配置（仅创建 .env，不启动服务）")
	reader := bufio.NewReader(in)
	// ask 在同一读取器上收集一行输入，默认值只用于明确提交的空行。
	ask := func(label, fallback string) (string, error) {
		fmt.Fprint(out, label)
		if fallback != "" {
			fmt.Fprintf(out, " [%s]", fallback)
		}
		fmt.Fprint(out, ": ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", errors.New("输入已中断，未创建配置")
		}
		value := strings.TrimSpace(line)
		if value == "" {
			value = fallback
		}
		return value, nil
	}
	var s Settings
	if s.BaseDomain, err = ask("项目基础域名", ""); err != nil {
		return err
	}
	s.BaseDomain = strings.ToLower(s.BaseDomain)
	if s.AdminDomain, err = ask("管理域名", "stack-nest."+s.BaseDomain); err != nil {
		return err
	}
	s.AdminDomain = strings.ToLower(s.AdminDomain)
	if s.AdminEmail, err = ask("管理员邮箱", ""); err != nil {
		return err
	}
	if s.AdminEmail, err = auth.NormalizeEmail(s.AdminEmail); err != nil {
		return errors.New("管理员邮箱格式无效")
	}
	if s.SMTPHost, err = ask("隐式 TLS SMTP 主机", "smtp.qq.com"); err != nil {
		return err
	}
	s.SMTPHost = strings.ToLower(s.SMTPHost)
	port, err := ask("SMTP 端口", "465")
	if err != nil {
		return err
	}
	if s.SMTPPort, err = strconv.Atoi(port); err != nil {
		return errors.New("SMTP 端口须为 1–65535 的整数")
	}
	if s.SMTPUser, err = ask("SMTP 发件邮箱", s.AdminEmail); err != nil {
		return err
	}
	if s.SMTPUser, err = auth.NormalizeEmail(s.SMTPUser); err != nil {
		return errors.New("SMTP 发件邮箱格式无效")
	}
	fmt.Fprint(out, "SMTP 密码 / 授权码（不回显）: ")
	s.SMTPPassword, err = password()
	fmt.Fprintln(out)
	if err != nil {
		return errors.New("密码输入已中断，未创建配置")
	}
	content, err := Render(string(template), s)
	if err != nil {
		return err
	}
	if err = Create(dir, content); err != nil {
		return err
	}
	fmt.Fprintln(out, "已创建 .env，数据库密码与认证密钥已自动生成。\n请按部署文档配置域名与证书，再运行 docker compose config --quiet。\n升级和迁移请保留这个文件。")
	return nil
}
