# StackNest Server

[文档导航](docs/README.md) · [架构与维护边界](docs/ARCHITECTURE.md) · [日常运维](docs/OPERATIONS.md)。

部署入口：[下载版本镜像部署](docs/INSTALL_RELEASE.md) · [源码 Docker 部署](docs/GETTING_STARTED.md) · [家用电脑 + Cloudflare Tunnel](docs/HOME_SERVER_TUNNEL.md)。四个常驻容器，一个 Nginx；普通公网 HTTPS 是默认方案。

StackNest 面向个人开发者和中小型公司，是在自有服务器上部署和管理私有项目的工具，支持静态站点及单容器 Docker HTTP 应用。后端使用 Go、PostgreSQL、Ent 和 Atlas；Web 管理端通过邮件验证码登录，并通过 OpenAPI 契约与后端协作。

以可信成员和单服务器为当前使用场景，优先保证易部署、易维护、易迁移。后续功能按实际工作流和维护成本取舍，产品边界见 [需求文档](docs/REQUIREMENTS.md#1-目标)。

项目归属由 PostgreSQL 的用户 ID 字段记录。普通用户只管理自己的项目和相关操作日志，管理员可管理全部项目；公开站点本身无需登录。

管理员添加邮箱时可以勾选“发送邀请邮件”，默认不勾选。超管在添加确认弹窗中还可选择“管理员”，默认仍为普通用户；普通管理员不能授予管理员角色。邀请包含固定登录地址和验证码登录说明；发信未能确认时邮箱仍已添加，界面提示手动告知登录地址。

| 需要做什么                               | 阅读入口                                                                              |
| ---------------------------------------- | ------------------------------------------------------------------------------------- |
| 部署 Docker HTTP 应用                    | [Docker 应用说明](docs/DOCKER_APPS.md)                                                |
| 超管配置应用联网并保护宿主局域网         | [网络保护配置](docs/NETWORK_EGRESS.md)                                                |
| 了解范围和验收标准                       | [需求文档](docs/REQUIREMENTS.md)                                                      |
| 在服务器首次部署、升级、备份或恢复       | [后端与联合部署](docs/DEPLOYMENT.md)                                                  |
| 把现有实例搬到另一台服务器               | [完整迁移步骤](docs/MIGRATION.md)                                                     |
| 没有公网 IP，通过 Cloudflare Tunnel 访问 | [Tunnel 配置与维护](docs/CLOUDFLARE_TUNNEL.md)                                        |
| 构建或单独更新管理端                     | [Web 部署文档](https://github.com/zby909/stack-nest-web/blob/main/docs/DEPLOYMENT.md) |
| 本地运行、修改和测试后端                 | 本文                                                                                  |
| 查询接口字段与状态码                     | [OpenAPI 契约](openapi/openapi.yaml)                                                  |

首次给自己的服务器安装，优先读 [版本镜像部署](docs/INSTALL_RELEASE.md)；维护者发布版本见 [GitHub Actions 发布说明](docs/RELEASING.md)。角色调整接口补充契约见 [user-roles.yaml](openapi/user-roles.yaml)。

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

当前配套版本为 API 0.9.0，新增草稿预览、确认发布、版本说明与项目详情，需先应用 `20261009090000_release_notes.sql`。详见下文版本流程。

API 0.6.0 增加站点暂停/恢复，以及按权限过滤的服务端日志查询与分页；前后端应一起更新，无新增数据库迁移。保留项目编辑、部署 ZIP 导出和管理员备份状态。HTTPS 会话使用 `__Host-` 前缀，ZIP 索引解析另有限额。响应模型和路由分别生成在 `internal/generated/models.gen.go`、`server.gen.go`，仍使用同一生成命令。

## 3. 检查与测试

```bash
go fmt ./...
go test ./...
go vet ./...
go build ./cmd/stacknest
```

Windows 可使用 `./scripts/check.ps1` 一次完成 Go 静态检查、测试、Python 备份/迁移工具测试及构建；默认不会启动数据库。使用完整隔离环境时：

```powershell
.\scripts\check.ps1 -Integration -PostgresBin 'D:\software\pgsql\bin' -Atlas '.\.local-dev\tools\atlas.exe'
```

需要 Go、Python 3、PostgreSQL 17 和 Atlas；将参数改为实际安装路径，PostgreSQL 和 Atlas 在 PATH 中时可省略路径参数。没有依赖本机测试数据，失败即停止；Windows 不支持的 Linux shell 专项测试会标为跳过，应在 Linux 上用 `python3 -m unittest discover -s deploy -p 'test_*.py'` 补跑。Web 仓库对应命令为 `pnpm check`，说明见 [前端回归](https://github.com/zby909/stack-nest-web/blob/main/docs/TESTING.md)。

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

上传固定限制为 90 MiB 归档、200 MiB 解压内容和 5000 个条目。以数据目录所在文件系统的可用空间计算，已用或预计使用达到 90% 时暂停上传；准入时保守预留 412 MiB，并在接收、解压、发布阶段复查。操作记录保存在 `data/logs/activity.jsonl`，每份约 1 MiB、最多三份，界面对全部保留记录进行服务端查询和分页；HTTP 请求日志输出 JSON 到标准输出。

API 0.7.0 在项目的每项 `deployments[].files` 返回 `{bytes, count}`，按已发布普通文件实时统计，不包含原包、数据库、日志、备份或临时文件。读取时校验归属和部署编号，并与发布/回退互斥；缺失目录、未完成事务、链接或读取失败时省略该版 `files`，保持列表可用。缺失不代表零字节，不能参与准确的合计。此字段无需数据库迁移；仅适用于当前单实例和少量私人项目的规模。

## 断点续传与文件目录（API 0.8.0）

管理端创建/更新项目使用 `/api/uploads` 创建会话，再以 4 MiB 分片 `PATCH /api/uploads/{id}`；每次带 `Upload-Offset`，偏移冲突时查询 `GET` 后再传，完整接收和持久化一个分片才推进偏移。最后 `POST /api/uploads/{id}/complete` 验证整个文件 SHA-256 并进行 ZIP/RAR 校验和解压；当前管理端传 `preview: true` 保存待发布版，随后单独确认发布。此接口是应用内协议，不是 tus 协议；原 multipart 接口保留兼容。

- 同一账号、文件内容和项目参数可在同一浏览器恢复；项目页提示未完成上传并自动回填原表单，刷新后需重新选择原文件，按服务端进度继续。文件已全部接收时可不选文件确认结果；支持暂停、继续、放弃。浏览器只存元数据，存储受限时仅当前页面可恢复。摘要使用 Web Crypto，因此需 HTTPS 或 localhost。
- 会话保存在数据卷 `uploads/<UUID>/`，与静态站点目录隔离。保留固定 24 小时，服务启动和每小时清理；每人最多两项、全局八项未完成上传，包含完成收据的记录总数最多 128。完成或放弃后删除临时原包，完成收据保留至到期。
- 文件最大 90 MiB、解压最大 200 MiB、最多 5000 条目，以及写入后磁盘不能达到 90% 的原限制继续生效。
- 服务重启可以恢复已完成分片；传输中断时当前未完成分片需重传。发布使用会话对应的固定部署编号，重复确认不会再生成一个版本。正在发布时不提供取消，连接断开后仍尝试完成事务；异常时保留恢复现场。
- `GET /api/projects/{slug}/files?deploymentId=...` 返回所选保留版本的相对路径与字节数；普通用户仅能查看自己的项目，管理员可查看全部。只列普通文件，不展示服务器绝对路径或执行文件内容。
- 未完成上传不是已发布项目，不包含在备份和迁移包中；迁移到另一台服务器后需重新上传尚未完成的文件。当前实现只支持单个 API 实例。

两个仓库的 `.github/workflows/check.yml` 提交后会自动运行回归。后端使用临时 PostgreSQL 17、现有 Atlas 迁移、Go race 检查和备份/迁移工具测试；CI 的数据库密码仅用于当次临时环境，不需要 SSH、SMTP 或生产数据库凭据。该工作流不会自动部署。

## 项目版本与详情（API 0.9.0）

管理端创建或更新项目会先生成一个待发布版，填写可选更新说明后，在独立地址检查页面，再点击“发布此版本”。每个项目最多保留当前正式版、上一正式版和一个待发布版；草稿未发布时不改变正式站点。已有草稿时需先发布或丢弃，才可继续上传或回退。

- 详情页按 `/projects/{slug}/{overview|versions|files|activity|settings}` 访问，包含版本、文件变化、项目日志及设置；刷新和前后退保留当前页面。
- 版本预览地址为 `stack-nest--<版本 UUID>.<项目基础域名>`，本地和 SSH 使用 `.localhost:入口端口`。两个连字符是预留命名空间，避免与普通项目地址冲突；现有单级泛域名 DNS 与证书即可覆盖。
- 预览是持有地址即可访问的静态站点，不是私密分享。只提供数据库仍保留的版本；丢弃、删除或轮转清理后返回 404，暂停项目会关闭所有版本预览。正式发布和回退保留版本编号与该版预览 URL。
- 版本页按需比较新增、修改、删除文件；修改通过 SHA-256 判断，相同大小的内容变化也能识别。文件目录与 ZIP 导出支持草稿、当前和上一版。
- 草稿存放在 `data/drafts/<slug>/<部署编号>`，正式站点仍在 `sites`，上一版在 `previous`。发布利用同卷移动和恢复日志完成，备份及迁移包包含草稿。
- 原 multipart 和省略 `preview` 的 API 保留直接发布兼容行为；新管理端使用显式草稿流程。更新说明最多 4096 个 UTF-8 字节，按纯文本显示。

升级需同步部署 Go、Web、Nginx 模板和新增数据库迁移。不能在存在草稿或未完成版本事务时直接退回 0.8；回退程序版本应恢复配套数据库与文件备份。

## 4. Go 运行时配置

| 名称                                              | 本地默认值                                                                 | 用途                                                                                              |
| ------------------------------------------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `STACKNEST_ADDR`                                  | `127.0.0.1:8080`                                                           | HTTP 监听地址；生产 Compose 显式设为 `:8080`，只供容器网络访问                                    |
| `STACKNEST_DATABASE_URL`                          | `postgres://stacknest:stacknest@localhost:15432/stacknest?sslmode=disable` | PostgreSQL 连接地址                                                                               |
| `STACKNEST_DATA_DIR`                              | `./data`                                                                   | 站点、删除暂存及上传临时目录；相对当前工作目录                                                    |
| `STACKNEST_WEB_DIR`                               | `../web/dist`                                                              | Go 直接提供已打包管理页时的目录；生产前端由 Nginx 提供                                            |
| `STACKNEST_BASE_DOMAIN`                           | 空                                                                         | 空值生成本地预览地址；生产设置后生成 `stack-nest-{slug}.{域名}`                                   |
| `STACKNEST_ADMIN_DOMAIN`                          | 空                                                                         | 用于生成邀请中的正式 HTTPS 登录地址，Compose 同时用于 Nginx                                       |
| `STACKNEST_INVITATION_LOGIN_URL`                  | 空                                                                         | 可选覆盖邀请地址；未覆盖时使用 `https://管理域名/login`，仅接受无凭据、查询参数或片段的 HTTPS URL |
| `STACKNEST_AUTH_ADMIN_EMAIL`                      | 无                                                                         | 首次启动时创建的管理员邮箱；后续启动须与数据库现有管理员匹配                                      |
| `STACKNEST_AUTH_SUPER_ADMIN_EMAIL`                | 同初始管理员邮箱                                                           | 指定一位已有管理员为超管；仅超管可配置网络、实例资源预算和新增管理员                              |
| `STACKNEST_AUTH_INITIAL_USER_EMAIL`               | 空                                                                         | 用户表为空时一并导入的普通用户邮箱                                                                |
| `STACKNEST_AUTH_SECRET`                           | 无                                                                         | 至少 32 字节的随机密钥，用于验证码摘要                                                            |
| `STACKNEST_SMTP_HOST` / `STACKNEST_SMTP_PORT`     | `smtp.qq.com` / `465`                                                      | 隐式 TLS SMTP 服务                                                                                |
| `STACKNEST_SMTP_USER` / `STACKNEST_SMTP_PASSWORD` | 无                                                                         | 发件邮箱与 SMTP 授权码，仅留在服务端                                                              |

`STACKNEST_POSTGRES_PASSWORD` 属于 Compose / PostgreSQL 配置，不是 Go 直接读取的设置。未配置可用邀请地址时，添加邮箱仍可用，“发送邀请邮件”被禁用；邀请地址不取自浏览器请求，因此 SSH 模式也不会给收件人发送 `localhost`。公网域名需已可访问，备案期间请先手动安排 SSH 访问。上传限制目前固定在代码中，数值与规则见 [需求文档](docs/REQUIREMENTS.md)。

Go 默认仅监听本机，且所有项目管理 API 都要求登录。当前不自动配置 HTTPS；对外部署时必须按 [部署文档](docs/DEPLOYMENT.md) 提供 HTTPS，或保持 SSH 隧道访问。
