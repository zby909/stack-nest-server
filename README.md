# StackNest Server

StackNest 是个人自托管的静态项目控制中心。后端使用 Go、PostgreSQL、Ent 和 Atlas；Web 管理端通过邮件验证码登录，并通过 OpenAPI 契约与后端协作。

项目归属由 PostgreSQL 的用户 ID 字段记录。普通用户只管理自己的项目和相关操作日志，管理员可管理全部项目；公开站点本身无需登录。

| 需要做什么                         | 阅读入口                                  |
| ---------------------------------- | ----------------------------------------- |
| 了解范围和验收标准                 | [需求文档](docs/REQUIREMENTS.md)          |
| 在服务器首次部署、升级、备份或恢复 | [后端与联合部署](docs/DEPLOYMENT.md)      |
| 构建或单独更新管理端               | [Web 部署文档](https://github.com/zby909/stack-nest-web/blob/main/docs/DEPLOYMENT.md) |
| 本地运行、修改和测试后端           | 本文                                      |
| 查询接口字段与状态码               | [OpenAPI 契约](openapi/openapi.yaml)      |

## 1. 本地开发

除 Web 命令外，本节命令都在 `server/` 执行。准备满足 [go.mod](go.mod) 的 Go 版本、Docker Compose 和 Atlas CLI；Go 建议与 [Dockerfile](Dockerfile) 保持一致，Atlas CLI 与 [迁移镜像](deploy/migrate/Dockerfile) 保持在 1.3.3。Atlas CLI 独立安装，不需要作为应用运行时依赖引入。

本地只需 PostgreSQL、Go API 和 Vite，不启动生产 Nginx：

```text
浏览器 -> Vite（web） -> Go API :8080 -> PostgreSQL :15432
```

### 启动数据库

```bash
docker compose -p stacknest-dev -f compose.dev.yaml up -d --wait
```

开发数据库只监听宿主机 `127.0.0.1:15432`，用户名、密码和数据库名均为 `stacknest`。固定使用项目名 `stacknest-dev`，避免与生产 Compose 的同名服务混用；已有本地数据库若使用其他项目名，应继续使用原名称以复用原卷。

### 应用迁移并运行 Go

Ubuntu / macOS Bash：

```bash
export STACKNEST_DATABASE_URL='postgres://stacknest:stacknest@localhost:15432/stacknest?sslmode=disable'
set -a; source ./.env.auth.local; set +a
atlas migrate apply --env local
go run ./cmd/stacknest
```

Windows PowerShell：

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

首次运行前参考 `.env.example` 创建不提交的 `.env.auth.local`，填写管理员邮箱、可选初始普通用户邮箱、至少 32 字节随机认证密钥，以及 QQ 邮箱 SMTP 授权码。SMTP 使用 `smtp.qq.com:465` 隐式 TLS；授权码不是 QQ 登录密码。用户表为空时才导入初始邮箱，之后在管理端维护名单。Go 不会自动读取 `.env`，也不会在启动时自动创建表；必须把所需值设为进程环境变量，并先应用 migration。生产 Compose 的 `.env.compose.example` 用途不同，不能与本地连接地址混用。

另开终端检查 `http://localhost:8080/api/health`：API 与数据库连接正常返回 200，连接失败或 2 秒探测超时返回 503。未登录访问 `/api/projects` 应返回 401。启动 Web 后输入白名单邮箱、收取验证码登录，再验证项目列表；健康检查不验证迁移或认证流程。

### 启动 Web

在同级 `web/` 按 [Web README](https://github.com/zby909/stack-nest-web/blob/main/README.md) 运行 `pnpm dev`。Vite 将 `/api`、`/sites` 代理到 Go；OpenAPI 文档直接访问 `http://localhost:8080/openapi.yaml`。

本机管理入口会生成 `http://stack-nest-{slug}.localhost:5173/` 预览地址，端口跟随当前入口；现代浏览器将 `.localhost` 解析到回环地址。Vite 将这个独立主机上的页面和资源交给 Go，支持 `/assets/main.js` 等绝对路径。后台地址下的 `/sites/` 返回 404，预览主机不能调用管理 API。SSH 入口也使用同样隔离；公网域名仍须单独验收。

停止开发数据库但保留数据：

```bash
docker compose -p stacknest-dev -f compose.dev.yaml down
```

## 2. 代码结构与修改方式

```text
cmd/stacknest/             # 启动、连接数据库、删除恢复与优雅退出
internal/config/          # 环境变量
internal/auth/            # 邮件验证码、SMTP、会话规则
internal/httpserver/      # HTTP 适配和本地静态预览
internal/project/         # 业务规则；依赖 Store / Publisher 接口
internal/postgres/        # Store 的 PostgreSQL + Ent 实现
internal/staticdeploy/    # ZIP/RAR 校验、发布和删除恢复
internal/storage/         # 数据盘容量探测、上传准入与写入保护
internal/activity/        # 独立于项目生命周期的轮转操作日志
internal/ent/schema/      # 手写 Ent schema
internal/ent/             # 生成的 ORM 代码
internal/generated/       # 生成的 HTTP 路由和响应模型
openapi/                  # API 契约及生成配置
migrations/               # SQL migration 与 atlas.sum
deploy/                   # 迁移镜像和 Nginx 模板
compose.dev.yaml          # 本地开发数据库
compose.yaml              # 生产联合部署
```

业务层不依赖 HTTP、Ent 或 PostgreSQL 的具体实现。生产 Nginx 使用固定模板读取共享文件，Go 不生成 Nginx 配置或触发 reload。

修改数据库结构：

1. 编辑 `internal/ent/schema/`。
2. 执行 `go generate ./internal/ent`。
3. 设置 `STACKNEST_DATABASE_URL` 后执行 `atlas migrate diff <name> --env local`，将 `<name>` 换为本次变更名称；生成差异使用 Docker 临时数据库，需要 Docker 可用。
4. 审查 SQL 和 `atlas.sum`，在开发库执行 `atlas migrate apply --env local`，运行测试。
5. 将 schema、生成代码、SQL 和校验文件作为同一项变更维护；不要手工修改已在生产应用的迁移。

修改 API 契约：

```bash
go generate ./openapi
go test ./...
```

先编辑 `openapi/openapi.yaml`，再生成和检查代码；不要直接修改生成文件。Apifox 导入该文件或服务的 `/openapi.yaml`，不另行维护第二套定义。在线契约中的默认 server URL 是本地地址，远程调试时需在 API 客户端选择实际管理域名。

## 3. 检查与测试

```bash
go fmt ./...
go test ./...
go vet ./...
go build ./cmd/stacknest
```

`go test ./...` 在未设置 `STACKNEST_TEST_DATABASE_URL` 时会跳过 PostgreSQL 集成测试，不能据此宣称真实数据库读写已通过。
邮件登录的完整 HTTP / PostgreSQL 流程另用 `STACKNEST_TEST_AUTH_DATABASE_URL` 指向独立且已迁移的 `stacknest_auth_test` 数据库验证；该测试会清空该专用库的登录用户表，不可指向开发库或生产库。

集成测试使用已迁移的独立测试库；以下为 Bash 示例，建库只需执行一次：

```bash
docker compose -p stacknest-dev -f compose.dev.yaml exec postgres createdb -U stacknest stacknest_test
STACKNEST_DATABASE_URL='postgres://stacknest:stacknest@localhost:15432/stacknest_test?sslmode=disable' atlas migrate apply --env local
STACKNEST_TEST_DATABASE_URL='postgres://stacknest:stacknest@localhost:15432/stacknest_test?sslmode=disable' go test ./internal/postgres -run TestStoreIntegration -v
```

邮件登录集成测试使用另一个专用测试库：

```bash
docker compose -p stacknest-dev -f compose.dev.yaml exec postgres createdb -U stacknest stacknest_auth_test
STACKNEST_DATABASE_URL='postgres://stacknest:stacknest@localhost:15432/stacknest_auth_test?sslmode=disable' atlas migrate apply --env local
STACKNEST_TEST_AUTH_DATABASE_URL='postgres://stacknest:stacknest@localhost:15432/stacknest_auth_test?sslmode=disable' go test ./internal/httpserver -run TestEmailAuthIntegration -v
```

PowerShell 使用 `$env:变量名='值'` 设置对应环境变量。不要把测试连接指向生产库。测试会创建并删除自己的临时项目，数据库本身保留供后续测试使用。

Windows 已安装 PostgreSQL 17 时，也可以运行隔离测试脚本；把路径换为实际程序路径：

```powershell
.\scripts\test-integration.ps1 -PostgresBin 'D:\software\pgsql\bin' -Atlas '.\.local-dev\tools\atlas.exe'
```

脚本在 `.local-dev/integration-*` 创建临时数据库，只监听 `127.0.0.1:15439`，应用迁移后执行全部测试，最后停止并删除该临时实例，不使用现有开发库。

上传固定限制为 90 MiB 归档、200 MiB 解压内容和 5000 个条目。以数据目录所在文件系统的可用空间计算，已用或预计使用达到 90% 时暂停上传；准入时保守预留 412 MiB，并在接收、解压、发布阶段复查。操作记录保存在 `data/logs/activity.jsonl`，每份约 1 MiB、最多三份，界面显示最近 100 条；HTTP 请求日志输出 JSON 到标准输出。

## 4. Go 运行时配置

| 名称                                              | 本地默认值                                                                 | 用途                                                            |
| ------------------------------------------------- | -------------------------------------------------------------------------- | --------------------------------------------------------------- |
| `STACKNEST_ADDR`                                  | `127.0.0.1:8080`                                                           | HTTP 监听地址；生产 Compose 显式设为 `:8080`，只供容器网络访问  |
| `STACKNEST_DATABASE_URL`                          | `postgres://stacknest:stacknest@localhost:15432/stacknest?sslmode=disable` | PostgreSQL 连接地址                                             |
| `STACKNEST_DATA_DIR`                              | `./data`                                                                   | 站点、删除暂存及上传临时目录；相对当前工作目录                  |
| `STACKNEST_WEB_DIR`                               | `../web/dist`                                                              | Go 直接提供已打包管理页时的目录；生产前端由 Nginx 提供          |
| `STACKNEST_BASE_DOMAIN`                           | 空                                                                         | 空值生成本地预览地址；生产设置后生成 `stack-nest-{slug}.{域名}` |
| `STACKNEST_AUTH_ADMIN_EMAIL`                      | 无                                                                         | 首次启动时创建的管理员邮箱；后续启动须与数据库现有管理员匹配    |
| `STACKNEST_AUTH_INITIAL_USER_EMAIL`               | 空                                                                         | 用户表为空时一并导入的普通用户邮箱                              |
| `STACKNEST_AUTH_SECRET`                           | 无                                                                         | 至少 32 字节的随机密钥，用于验证码摘要                          |
| `STACKNEST_SMTP_HOST` / `STACKNEST_SMTP_PORT`     | `smtp.qq.com` / `465`                                                      | 隐式 TLS SMTP 服务                                              |
| `STACKNEST_SMTP_USER` / `STACKNEST_SMTP_PASSWORD` | 无                                                                         | 发件邮箱与 SMTP 授权码，仅留在服务端                            |

`STACKNEST_ADMIN_DOMAIN` 和 `STACKNEST_POSTGRES_PASSWORD` 属于 Compose / Nginx / PostgreSQL 配置，不是 Go 直接读取的设置。上传限制目前固定在代码中，数值与规则见 [需求文档](docs/REQUIREMENTS.md)。

Go 默认仅监听本机，且所有项目管理 API 都要求登录。当前不自动配置 HTTPS；对外部署时必须按 [部署文档](docs/DEPLOYMENT.md) 提供 HTTPS，或保持 SSH 隧道访问。
