package doctor

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"io"
	"os"
	"path/filepath"
	"stacknest/server/internal/auth"
	"stacknest/server/internal/config"
	"stacknest/server/internal/containerapp"
	"stacknest/server/internal/egress"
	"stacknest/server/internal/storage"
	"time"
)

// Check 只记录固定的排障建议，原始错误可能带连接串或密码，不允许写入报告。
type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}
type Report struct {
	Checks []Check `json:"checks"`
	Failed bool    `json:"failed"`
}

// Main 输出人类可读或 JSON 的部署诊断，失败返回非零，警告不阻止已选择的可选部署。
func Main(ctx context.Context, cfg config.Config, args []string, out, errOut io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(errOut)
	asJSON := flags.Bool("json", false, "输出 JSON，不含配置秘密")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errOut, "doctor 只接受 --json")
		return 2
	}
	report := Inspect(ctx, cfg)
	if *asJSON {
		if json.NewEncoder(out).Encode(report) != nil {
			return 2
		}
	} else {
		for _, check := range report.Checks {
			fmt.Fprintf(out, "[%s] %s: %s\n", check.Status, check.Name, check.Message)
		}
	}
	if report.Failed {
		return 1
	}
	return 0
}

// add 汇总每项状态，保持结果顺序稳定。
func (r *Report) add(name, status, message string) {
	r.Checks = append(r.Checks, Check{name, status, message})
	if status == "fail" {
		r.Failed = true
	}
}

// Inspect 使用运行中的 API 相同环境配置，仅执行读取和有时限的连接检查。
func Inspect(ctx context.Context, cfg config.Config) Report {
	report := Report{Checks: []Check{}}
	if auth.ValidateSecret(cfg.AuthSecret) != nil {
		report.add("auth", "fail", "请配置至少 32 字节随机认证密钥，不能使用公开示例值")
	} else {
		report.add("auth", "pass", "认证密钥通过配置检查，内容不会输出")
	}
	task, cancel := context.WithTimeout(ctx, 5*time.Second)
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err == nil {
		err = db.PingContext(task)
	}
	if err != nil {
		report.add("database", "fail", "无法连接数据库，请检查数据库地址、凭据和服务状态")
	} else {
		var valid bool
		err = db.QueryRowContext(task, `SELECT to_regclass('auth_users') IS NOT NULL AND to_regclass('projects') IS NOT NULL AND to_regclass('container_apps') IS NOT NULL AND to_regclass('app_limits') IS NOT NULL`).Scan(&valid)
		if err != nil || !valid {
			report.add("schema", "fail", "缺少必要数据库表，请先执行迁移")
		} else {
			report.add("database", "pass", "连接正常，必要数据表存在；升级仍须执行迁移")
		}
		var adminRole, superRole string
		err = db.QueryRowContext(task, "SELECT role FROM auth_users WHERE email=$1", cfg.AuthAdminEmail).Scan(&adminRole)
		superErr := db.QueryRowContext(task, "SELECT role FROM auth_users WHERE email=$1", cfg.AuthSuperAdminEmail).Scan(&superRole)
		if err != nil || superErr != nil || adminRole != auth.RoleAdmin || superRole != auth.RoleAdmin {
			report.add("roles", "fail", "部署配置指定的管理员和超管必须是已有管理员账号")
		} else {
			report.add("roles", "pass", "管理员及超管配置与数据库一致")
		}
	}
	if db != nil {
		db.Close()
	}
	cancel()
	usage, err := storage.ReadUsage(cfg.DataDir)
	if err != nil {
		report.add("storage", "fail", "无法读取数据目录，请检查卷挂载和权限")
	} else if storage.CheckUsage(usage, storage.UploadBudget) != nil {
		report.add("storage", "warn", "可用空间不足以接收新上传，请检查磁盘")
	} else {
		report.add("storage", "pass", fmt.Sprintf("数据卷剩余 %.1f GiB，上传空间预留足够", float64(usage.FreeBytes)/(1<<30)))
	}
	if cfg.RunnerSocket == "" {
		report.add("docker", "warn", "未配置 Runner，仅启用静态站点")
	} else {
		value, err := containerapp.NewClient(cfg.RunnerSocket).Diagnostics(ctx)
		if err != nil {
			report.add("docker", "fail", "Runner / Docker / 配额 / 网络保护未就绪，请检查 runner 日志")
		} else {
			report.add("docker", "pass", "Docker 可达，存储驱动："+value.Driver)
			if value.DataQuota && value.WritableQuota {
				report.add("quota", "pass", "XFS 项目配额正在记账并强制执行；应用可写层使用 overlay2 硬配额")
			} else {
				report.add("quota", "warn", "尚未同时启用可写层和持久化数据硬配额，参见 DOCKER_APPS.md")
			}
			if !value.NetworkProtected {
				report.add("network-protection", "warn", "Runner 未启用宿主局域网保护")
			}
		}
	}
	if cfg.EgressSocket != "" {
		task, cancel := context.WithTimeout(ctx, 5*time.Second)
		view, err := egress.NewClient(cfg.EgressSocket).Get(task)
		cancel()
		if err != nil || !view.Ready {
			report.add("network", "fail", "宿主网络服务未就绪，请检查 stacknest-egress 服务")
		} else {
			report.add("network", "pass", fmt.Sprintf("局域网保护已就绪，当前覆盖 %d 个网段", len(view.EffectiveBlockedCIDRs)))
		}
	} else {
		report.add("network", "warn", "未配置可选的宿主网络管理服务")
	}
	if _, err := auth.NewSMTPMailer(auth.SMTPConfig{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUser, Password: cfg.SMTPPassword}); err != nil {
		report.add("smtp", "fail", "SMTP 参数不完整或无效，请配置隐式 TLS SMTP 服务")
	} else if probeSMTP(ctx, cfg) != nil {
		report.add("smtp", "fail", "SMTP TLS / 认证失败，请检查网络、证书和授权码（未发送邮件）")
	} else {
		report.add("smtp", "pass", "TLS 与 SMTP 认证成功；未发送邮件，不代表最终邮件送达")
	}
	if _, err := auth.NormalizeInvitationURL(cfg.InvitationLoginURL); err != nil {
		report.add("invitation", "warn", "邀请地址未配置或无效，可登录但无法发邀请")
	} else {
		report.add("invitation", "pass", "邀请地址格式有效；公网 DNS、证书和路由需另行验收")
	}
	// 只检查存在性，不通过 Guard.Usage 创建新目录。
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "index.html")); err != nil {
		report.add("frontend", "warn", "API 容器内未发现前端；若由独立 Nginx 提供则正常，否则请构建前端")
	} else {
		report.add("frontend", "pass", "前端入口存在")
	}
	return report
}
