package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sample 提供包含插值字符、引号、反斜杠和空格的虚构 SMTP 授权码。
func sample() Settings {
	return Settings{BaseDomain: "example.com", AdminDomain: "stack-nest.example.com", AdminEmail: "admin@example.com",
		SMTPHost: "smtp.example.com", SMTPPort: 465, SMTPUser: "sender@example.com", SMTPPassword: `fake $HOME ${TOKEN} $$ # ' " \ code\`}
}

// template 读取发布流程实际使用的公开模板，并附上固定镜像标记。
func template(t *testing.T) string {
	t.Helper()
	value, err := os.ReadFile("../../.env.compose.example")
	if err != nil {
		t.Fatal(err)
	}
	return string(value) + "\nSTACKNEST_APP_IMAGE=ghcr.io/example/app@sha256:" + strings.Repeat("a", 64) + "\n"
}

// TestComposeRoundTrip 使用真实 Compose 解析验证特殊密码、数据库连接和密钥一致性。
func TestComposeRoundTrip(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is required (no daemon needed)")
	}
	settings := sample()
	content, err := Render(template(t), settings)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := Create(dir, content); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  check:\n    image: ${STACKNEST_APP_IMAGE}\n    environment:\n"
	for _, key := range []string{"STACKNEST_SMTP_PASSWORD", "STACKNEST_POSTGRES_PASSWORD", "STACKNEST_DATABASE_URL", "STACKNEST_AUTH_SECRET"} {
		compose += "      " + key + ": ${" + key + "}\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "check.yaml"), []byte(compose), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(docker, "compose", "--env-file", filepath.Join(dir, ".env"), "-f", filepath.Join(dir, "check.yaml"), "-p", "setup-check", "config", "--format", "json")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "STACKNEST_") && !strings.HasPrefix(value, "COMPOSE_") {
			command.Env = append(command.Env, value)
		}
	}
	raw, err := command.Output()
	if err != nil {
		t.Fatal("Compose could not parse generated config:", err)
	}
	var result struct {
		Services map[string]struct {
			Image       string
			Environment map[string]string
		}
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	env := result.Services["check"].Environment
	// config 输出可再次作为 Compose 文件，美元符号会被重新转义；--environment 是实际插值值。
	if env["STACKNEST_SMTP_PASSWORD"] != strings.ReplaceAll(settings.SMTPPassword, "$", "$$") {
		t.Fatal("unexpected serialized SMTP password")
	}
	args := append(append([]string{}, command.Args[1:len(command.Args)-2]...), "--environment")
	actual := exec.Command(docker, args...)
	actual.Env = command.Env
	raw, err = actual.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "STACKNEST_SMTP_PASSWORD="+settings.SMTPPassword+"\n") {
		t.Fatal("SMTP password changed during interpolation")
	}
	if env["STACKNEST_DATABASE_URL"] != "postgres://stacknest:"+env["STACKNEST_POSTGRES_PASSWORD"]+"@postgres:5432/stacknest?sslmode=disable" {
		t.Fatal("database password mismatch")
	}
	if len(env["STACKNEST_POSTGRES_PASSWORD"]) != 64 || len(env["STACKNEST_AUTH_SECRET"]) != 64 {
		t.Fatal("missing generated credentials")
	}
	if !strings.HasSuffix(result.Services["check"].Image, "@sha256:"+strings.Repeat("a", 64)) {
		t.Fatal("pinned image lost")
	}
}

// TestCreateNeverOverwrites 同名文件与符号链接均拒绝覆盖，Linux 上限制其他用户读取。
func TestCreateNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := Create(dir, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := Create(dir, []byte("second")); err == nil {
		t.Fatal("existing config overwritten")
	}
	value, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if string(value) != "first" {
		t.Fatal("old config changed")
	}
	info, _ := os.Stat(filepath.Join(dir, ".env"))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("insecure config mode")
	}
	linkDir := t.TempDir()
	if err := os.Symlink(filepath.Join(linkDir, "missing"), filepath.Join(linkDir, ".env")); err == nil {
		if err := Create(linkDir, []byte("secret")); err == nil {
			t.Fatal("symlink followed")
		}
	}
}

// TestPromptDoesNotExposeSecrets 验证完整问答使用默认值、拒绝二次初始化且不打印密钥。
func TestPromptDoesNotExposeSecrets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.compose.example"), []byte(template(t)), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	password := sample().SMTPPassword
	err := Run(dir, strings.NewReader("example.com\n\nadmin@example.com\n\n\n\n"), &output, func() (string, error) { return password, nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), password) {
		t.Fatal("password printed")
	}
	before, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if err := Run(dir, strings.NewReader(""), &output, nil); err == nil {
		t.Fatal("existing config accepted")
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !bytes.Equal(before, after) {
		t.Fatal("existing config changed")
	}
}

// TestInvalidConfigAndTemplate 拒绝错误域名、注入行与残缺模板，每次生成独立随机凭据。
func TestInvalidConfigAndTemplate(t *testing.T) {
	source := template(t)
	for _, change := range []func(*Settings){
		func(s *Settings) { s.BaseDomain = "https://example.com" },
		func(s *Settings) { s.BaseDomain = "127.0.0.1" },
		func(s *Settings) { s.AdminDomain = "elsewhere.org" },
		func(s *Settings) { s.AdminDomain = "stack-nest-demo.example.com" },
		func(s *Settings) { s.AdminEmail = "not-an-email" },
		func(s *Settings) { s.SMTPHost = "smtp.example.com;" },
		func(s *Settings) { s.SMTPPort = 0 },
		func(s *Settings) { s.SMTPPassword = "\nBAD=value" },
	} {
		s := sample()
		change(&s)
		if _, err := Render(source, s); err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
	for _, source := range []string{"", source + "\nSTACKNEST_SMTP_HOST=duplicate"} {
		if _, err := Render(source, sample()); err == nil {
			t.Fatal("invalid template accepted")
		}
	}
	first, _ := Render(source, sample())
	second, _ := Render(source, sample())
	if bytes.Equal(first, second) {
		t.Fatal("credentials reused")
	}
}
