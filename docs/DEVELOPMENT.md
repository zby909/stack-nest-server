# 开发与测试

两个仓库并排放置为 `server/`、`web/`。后端命令在 `server/` 执行。

准备满足 [go.mod](../go.mod) 的 Go 版本、Docker Compose 和 Atlas CLI；Atlas 版本与 [迁移镜像](../deploy/migrate/Dockerfile)一致。前端使用 Node.js 24 和其 `package.json` 指定的 pnpm。

## 本地运行

启动独立开发数据库：

```bash
docker compose -p stacknest-dev -f compose.dev.yaml up -d --wait
cp .env.example .env.auth.local
```

编辑 `.env.auth.local`，填写管理员邮箱、至少 32 字节随机认证密钥及可用的 SMTP。这个文件不提交。

Bash 中加载配置、应用迁移并运行：

```bash
export STACKNEST_DATABASE_URL='postgres://stacknest:stacknest@localhost:15432/stacknest?sslmode=disable'
set -a; source ./.env.auth.local; set +a
atlas migrate apply --env local
go run ./cmd/stacknest
```

Windows PowerShell 使用：

```powershell
$env:STACKNEST_DATABASE_URL='postgres://stacknest:stacknest@localhost:15432/stacknest?sslmode=disable'
Get-Content .env.auth.local | ForEach-Object {
  if ($_ -match '^([A-Z0-9_]+)=(.*)$') {
    [Environment]::SetEnvironmentVariable($Matches[1], $Matches[2], 'Process')
  }
}
atlas migrate apply --env local
go run ./cmd/stacknest
```

Go 读取进程环境变量，不自动加载配置文件或创建表。

另开终端，在 `web/` 执行 `pnpm install --frozen-lockfile`、`pnpm dev`。访问 `http://localhost:5173`，使用配置的邮箱登录。

- API：`http://localhost:8080/api/health`
- 契约：`http://localhost:8080/openapi.yaml`
- 静态预览：界面返回的 `stack-nest-{slug}.localhost:5173`

Docker 应用联调使用 [Compose 环境](GETTING_STARTED.md)。停止开发库并保留数据：`docker compose -p stacknest-dev -f compose.dev.yaml down`。

## 修改代码

- 业务规则在 `internal/project`、`internal/containerapp` 等模块；模块分工见 [架构](ARCHITECTURE.md)。
- API 路由在 `openapi/openapi.yaml`，模型在 `openapi/components.yaml`；修改后执行 `go generate ./openapi`，不要手改生成代码。服务端 `/openapi.yaml` 返回合并后的完整契约。宿主网络、角色、限额及 [平台诊断和版本](../openapi/platform.yaml) 使用同目录独立契约与手写路由。
- 修改 Ent schema 后执行 `go generate ./internal/ent`，再用 `atlas migrate diff 变更名 --env local` 生成并审查迁移。
- 手写 SQL 使用 `atlas migrate hash --dir file://migrations` 更新校验。已应用的迁移不改写。
- 同时修改 API、前端和数据库时，作为配套版本维护。

## 检查与测试

```bash
go test ./...
go vet ./...
go build ./cmd/stacknest
python3 -m unittest discover -s deploy -p 'test_*.py'
```

Windows 一键检查用 `./scripts/check.ps1`；带独立 PostgreSQL 的完整检查：

```powershell
.\scripts\check.ps1 -Integration -PostgresBin '你的PostgreSQL/bin' -Atlas '你的atlas.exe'
```

未设置测试数据库时，Go 会跳过 PostgreSQL 集成测试。需要手动运行时，先创建并迁移专用的 `stacknest_test`、`stacknest_auth_test`，分别设置 `STACKNEST_TEST_DATABASE_URL`、`STACKNEST_TEST_AUTH_DATABASE_URL`。测试会清理专用库，不可指向生产库。

GitHub Actions 自动准备临时 PostgreSQL、执行迁移和 Go race / vet / 漏洞检查。前端使用 [浏览器回归](https://github.com/zby909/stack-nest-web/blob/main/docs/TESTING.md)。

## 部署工具检查

准备配套的本地 `stacknest-app`、`stacknest-web`、`stacknest-migrate` 镜像（相同版本标签），在 `web/` 安装依赖及 Chromium 后执行：

```bash
python3 deploy/check_release.py --image-prefix 你的镜像前缀 --version vX.Y.Z
```

它创建全新的 PostgreSQL、临时 TLS 测试邮箱与 Nginx，启动真实浏览器验证登录、分片上传、预览、发布、更新和回退。邮件只留在测试收件箱，不发送到外部；端口随机绑定回环地址，结束后清理本次容器和数据卷。GitHub 发布流程在推送镜像前自动运行此检查。

```bash
python3 deploy/check_nginx.py --image 你的Web镜像
sudo python3 deploy/check_egress_network.py
```

这两项需要 Linux 环境，使用隔离夹具验证 Nginx 与网络规则。真实 SMTP、DNS、证书和恢复仍需目标环境验收。
