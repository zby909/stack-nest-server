# StackNest 后端与联合部署

本文是 **Ubuntu + Docker Compose 单实例部署**的操作入口，涵盖 Web、Go API、PostgreSQL、迁移和 Nginx。所有命令均在 `server/` 目录、Ubuntu Bash 中执行；备份文件的重定向命令不要直接复制到 Windows PowerShell。

- 功能范围与统一验收标准：[需求文档](REQUIREMENTS.md)。
- 前端构建、API 地址及仅更新前端：[Web 部署文档](https://github.com/zby909/stack-nest-web/blob/main/docs/DEPLOYMENT.md)。
- 本地 Go 开发、代码生成和测试：[Server README](../README.md)。

## 1. 准备目录和运行环境

准备 Ubuntu 22.04 / 24.04、可用的 Docker Engine 与 Compose Plugin。镜像内完成 Go、Node.js 和 Atlas 构建，宿主机无需安装这些开发工具。磁盘需容纳数据库、所有站点、上传临时文件、镜像及备份，按实际项目量预留空间。

```text
workspace/
  server/  # 在这里运行本文命令
  web/     # 必须是同级目录，Compose 使用 ../web 构建 Nginx 镜像
```

使用匹配的前后端版本，不要只准备 server 仓库。检查 Docker 可连接：

```bash
docker version
docker compose version
```

只需本地开发时，使用 README 中的 `compose.dev.yaml` 流程；不要把本地开发数据库当作生产数据库。

## 2. 配置环境变量

首次创建配置；已有 `.env` 时保留并逐项检查：

```bash
if [ ! -f .env ]; then cp .env.compose.example .env; fi
chmod 600 .env
```

编辑 `.env`，替换以下示例值：

```dotenv
STACKNEST_ADMIN_DOMAIN=stack-nest.example.com
STACKNEST_BASE_DOMAIN=example.com
STACKNEST_ORIGIN_CERT_DIR=./cert
STACKNEST_POSTGRES_PASSWORD=replace-with-long-random-password
STACKNEST_DATABASE_URL=postgres://stacknest:replace-with-long-random-password@postgres:5432/stacknest?sslmode=disable
STACKNEST_AUTH_ADMIN_EMAIL=admin@example.com
STACKNEST_AUTH_INITIAL_USER_EMAIL=user@example.com
STACKNEST_AUTH_SECRET=replace-with-at-least-32-random-characters
STACKNEST_SMTP_HOST=smtp.qq.com
STACKNEST_SMTP_PORT=465
STACKNEST_SMTP_USER=your-qq-address@qq.com
STACKNEST_SMTP_PASSWORD=replace-with-new-qq-smtp-authorization-code
```

| 配置                 | 要求                                                                                 |
| -------------------- | ------------------------------------------------------------------------------------ |
| 管理域名             | 完整域名，不带协议、端口或路径                                                       |
| 项目基础域名         | 不带 `*.`；项目地址为 `stack-nest-{slug}.基础域名`；管理域名应与项目域名区分         |
| 源站证书目录         | 仅 Cloudflare Full (strict) 入口需要；放匹配的 `certificate.pem` 和 `private.key`    |
| PostgreSQL 密码      | 必须替换示例密码；可用 `openssl rand -hex 32` 生成随机值                             |
| 数据库 URL           | 容器内使用 `postgres:5432`，不能填 `localhost`；密码必须与上一项一致                 |
| 管理员与初始用户邮箱 | 首次启动且用户表为空时导入；之后通过管理员界面增删普通用户，管理员邮箱不能在界面删除 |
| 认证密钥             | 至少 32 字节随机值，可用 `openssl rand -base64 48` 生成；用于验证码摘要              |
| QQ SMTP              | 发件邮箱完整地址，465 端口使用隐式 TLS；密码填新生成的 SMTP 授权码，不填 QQ 登录密码 |

密码含 `@`、`:`、`/`、`#` 等字符时，URL 中的密码部分须百分号编码；密码含 `$` 时还需注意 Compose 插值，使用随机十六进制密码可避免这两类问题。不要提交或公开 `.env`。

Compose 会读取 `.env`，同名 shell 环境变量优先级更高；本地开发时导出的数据库地址可能覆盖生产配置。详见 [Compose 环境变量规则](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/)。

```bash
docker compose config --quiet
```

没有输出且退出码为 0，说明 Compose 配置可解析；这一步不会验证 DNS、密码是否能连接数据库或容器是否能启动。不要把包含完整密码的 `docker compose config` 输出贴入日志或工单。

## 3. 先确定访问入口

项目管理 API 已有邮件验证码登录保护，但默认 `80:80` 仍只提供 HTTP。**公网管理入口必须有 HTTPS；尚未配置 HTTPS 时仅通过 SSH 隧道或受信任内网访问。**已发布的项目站点仍是公开资源。邮件登录不能替代传输加密。

DNS 将以下名称指向实际入口：

```text
stack-nest.example.com   A      -> 入口 IPv4
*.example.com            A      -> 入口 IPv4
```

只有实际打通 IPv6 时才添加 AAAA 记录。客户端也必须能解析这些名称；没有 DNS 的单机联调可为具体的管理域名和测试项目域名添加 hosts 记录，hosts 不支持泛域名。

默认 Compose 仅提供 HTTP 80，不配置证书或监听 HTTPS 443。Go 的 8080 和 PostgreSQL 的 5432 均未发布到宿主机。Cloudflare Full (strict) 公网入口按下节叠加 TLS 配置；已有宿主机 Nginx 等独立代理时按第 5 节配置。

### Cloudflare Full (strict) 公网入口

以 `zby909.top` 为例，管理端设为 `stack-nest.zby909.top`，项目使用 `stack-nest-{slug}.zby909.top`。Cloudflare 中需要代理开启的管理域名 A 记录和 `*.zby909.top` 泛域名 A 记录；精确 DNS 记录优先于泛域名，现有其他子域名不受此泛记录覆盖。浏览器到 Cloudflare 使用边缘证书，Cloudflare 到服务器的 443 使用源站证书；仅有边缘证书不足以满足 [Full (strict)](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/)。

在服务器 `server/cert/` 放置与 `*.zby909.top` 匹配的 `certificate.pem`、`private.key`。两个文件必须配对；私钥只留在服务器私有目录，不放 Git、Docker 构建产物或聊天记录。证书目录及私钥权限分别设为 `700`、`600`。在 `.env` 中设置 `STACKNEST_ORIGIN_CERT_DIR=./cert`，以及正确的管理域名和项目基础域名。公网部署时再设置：

```dotenv
COMPOSE_FILE=compose.yaml:compose.cloudflare.yaml
```

这样本文后续的 `docker compose` 命令会同时加载 Cloudflare 配置：80 仅重定向到 HTTPS，443 用证书提供管理端和项目站点，并通过 ALPN 支持 HTTP/2。Cloudflare 的 SSL/TLS 加密模式须为 **Full (strict)**；源站还须允许公网 TCP 80/443（包括云厂商安全组）。未匹配的 Host / 项目 slug 不会落到管理端。不要启用 Flexible，它会使 Cloudflare 到源站的流量保持明文。Nginx 到 Go 的容器内代理仍使用 HTTP/1.1；这与浏览器或 Cloudflare 到 Nginx 的协议版本彼此独立。

Cloudflare 的[请求上传限制](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/4xx-client-error/error-413/)还受套餐及站点设置影响。本项目统一限制归档为 90 MiB，Nginx 请求体为 91 MiB，为表单开销预留余量；边缘限制更低时仍可能返回 413。不要对管理域名设置“缓存所有内容”。项目响应明确使用 `Cache-Control: no-store`；若已有覆盖源站缓存头的 Cloudflare 规则，应移除覆盖，并清理旧缓存。模板只信任官方 Cloudflare 网段传来的访客 IP，后端据此限流；网段快照需随官方变化维护。

如果源站位于中国内地，Cloudflare 代理、HTTPS 边缘证书和二级域名均不能替代网站所需的 ICP 备案。阿里云的[备案说明](https://www.alibabacloud.com/help/en/icp-filing/basic-icp-service/product-overview/what-is-an-icp-filing)写明中国内地服务器上的域名网站需要备案，未备案时可能被阿里云阻断。上线前应先确认备案状态，或将公网源站迁至中国内地以外的服务器。

## 4. 首次启动并验证

```bash
docker compose up -d --build
docker compose ps -a
docker compose logs --tail=100 migrate stacknest nginx
docker compose exec nginx nginx -t
```

首次启动依次等待 PostgreSQL 健康、Atlas 迁移成功、Go 健康，再启动 Nginx。[Compose 依赖条件](https://docs.docker.com/compose/how-tos/startup-order/) 负责这个启动顺序。预期状态：

| 服务        | 正常状态               | 职责                                                   |
| ----------- | ---------------------- | ------------------------------------------------------ |
| `postgres`  | Up / healthy           | 保存项目和部署记录                                     |
| `migrate`   | Exited (0)             | 一次性执行镜像内的 `migrations/`，执行完退出是正常现象 |
| `stacknest` | Up / healthy           | API 与站点文件管理；不会自动创建或修改数据库 Schema    |
| `nginx`     | Up，且 `nginx -t` 成功 | 管理端静态资源、API 反代、项目泛域名                   |

在服务器上检查默认 80 端口入口：

```bash
curl -fsS -H 'Host: stack-nest.example.com' http://127.0.0.1/api/health
curl -i -H 'Host: stack-nest.example.com' http://127.0.0.1/api/projects
```

健康接口在 API 与数据库连接可用时返回 `{"status":"ok"}`，数据库连接异常时返回 503；它不检查迁移或登录。未登录的项目列表应返回 401。若使用第 5 节的内部端口，诊断容器入口时把 URL 改为 `http://127.0.0.1:18080/...`。在浏览器用管理员邮箱请求验证码并登录，项目列表成功加载后才能证明认证与数据库查询链路正常；再用普通用户邮箱登录，确认只能管理自己的项目、看不到其他人的项目和邮箱管理界面。

通过受保护入口打开管理域名并登录，上传包含 `index.html` 的测试 ZIP，slug 填 `demo`。刷新列表后打开 `stack-nest-demo.example.com`，检查页面、资源和 SPA 深层路径。再用另一个 slug 上传未加密的单卷 RAR，验证其站点同样可访问。公网入口统一使用 HTTPS。不要用 IP 首页能否打开来判断部署成功：未匹配域名的请求会返回 404。

保留这个项目完成重启与备份恢复验证，再测试删除。完整验收清单统一维护在 [需求文档第 8 节](REQUIREMENTS.md)。

## 5. 已有宿主机 Nginx / HTTPS

在 `compose.yaml` 中**替换** nginx 服务现有端口映射，不要追加第二个对公网开放的映射：

```yaml
ports:
  - "127.0.0.1:18080:80"
```

外层代理至少需要转发原始 Host、请求协议，并允许上传大小。下面仅展示受信任内网的 HTTP 转发；公网 TLS 证书需在外层另行配置，项目域名与管理域名可以采用不同访问策略。

```nginx
server {
    listen 80;
    server_name stack-nest.example.com *.example.com;
    client_max_body_size 91m;
    proxy_request_buffering off;

    location / {
        proxy_pass http://127.0.0.1:18080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

`91m` 为 90 MiB ZIP/RAR 和 multipart 表单预留空间；内层 Nginx 已使用同样限制，并关闭请求缓冲，使 Go 可以在接收文件前拒绝空间不足的请求。外层若保留 Nginx 默认的 1 MiB 限制，会先返回 413，见 [Nginx 上传大小说明](https://nginx.org/en/docs/http/ngx_http_core_module.html#client_max_body_size)。

TLS 入口需覆盖管理域名和项目泛域名。直接接收 HTTPS 的外层 Nginx 用 `$scheme` 传递协议，内层会保留它，Go 据此生成 HTTPS 项目链接；如果前面还有 TLS 网关，应按可信代理链传递真实协议。项目流量也必须经过相应 HTTPS 入口。本节示例不包含证书签发。

## 6. 数据、日志与备份

`GET /api/health` 无需登录，每次用最多 2 秒检查数据库连接：成功返回 `200 {"status":"ok"}`，失败或超时返回 `503 {"status":"unavailable"}`，响应禁止缓存。Compose 已使用这个地址进行健康检查，持续故障会按探针重试设置标记为 unhealthy；它不验证数据库迁移、SMTP 或每个站点，也不会仅因 unhealthy 自动重启容器。

`postgres-data` 保存数据库；`stacknest-data` 保存 `/data/sites` 当前站点、`previous` 私有上一版、`changes` 更新/回退事务、`trash` 删除暂存、`pending` 上传恢复标记、`logs` 操作日志和 `tmp` 临时文件。Go 可写，项目 Nginx 只读且只公开 `sites/{slug}`。实际卷名带 Compose 项目前缀，升级必须继续使用原项目名。`docker compose down -v` 会删除数据卷，不作为日常操作。

项目所有者由数据库的用户 ID 外键记录，站点目录名只是全局唯一 slug。升级到含 `project_owner` 迁移的版本前，先做第 6 节备份，再停止旧 API、应用迁移并启动新 API；不能让旧版 API 在迁移后继续接收上传。迁移把现有项目分配给管理员。删除普通用户会将其项目转交管理员，并使该用户会话失效；不会删除站点文件。

上传、更新、回退与删除都留下恢复依据，启动时结合数据库确认结果；数据库无法确认时保留现场并停止启动，不直接删文件。单个 API 实例独占写入数据卷。不要手动清理 `pending`、`changes` 或 `trash` 来释放 slug。更新和回退保留原地址，仅保留当前版和上一版；文件先完成校验再切换，切换期间可能有极短的请求失败窗口。旧版服务不理解 `changes`，存在未完成事务时不可直接降级。

管理页可查看最近 100 条上传、更新、回退、删除记录，包括失败原因和请求编号。管理员还可在 `/system` 查看 CPU、内存和数据卷空间；普通用户访问 `/api/system` 会收到 403，`/api/storage` 只返回上传策略，不返回磁盘容量。`/data/logs/activity.jsonl` 每份约 1 MiB，保留当前文件和两份轮转文件；删除项目不会删除日志。请求日志为 JSON，记录状态码、耗时和请求编号，Docker 使用 10 MiB × 3 轮转。日志不含验证码、Cookie 或 SMTP 授权码。

使用仓库脚本备份，参数是实际 Compose 文件和备份根目录：

```bash
bash deploy/backup.sh compose.yaml /opt/stacknest/backups
```

如果原服务通过 `-p stacknest` 启动，调用脚本时也设置 `STACKNEST_COMPOSE_PROJECT=stacknest`；不要换项目名。脚本短暂停止 API，保存同一批 `postgres.dump`、`files.tar.gz`（sites/trash/pending/logs/previous/changes），检查可读性并写入 SHA256SUMS，退出时恢复原本运行的 API。脚本默认保留最新 14 份完整备份，可用 `STACKNEST_BACKUP_KEEP` 调整；失败现场以 `.partial-` 开头，不作为有效备份，不自动删除。已有静态站点在备份期间仍由 Nginx 提供。

每日备份可使用 `deploy/stacknest-backup.service` 和 `.timer`。示例要求 `/opt/stacknest/current` 指向当前发布目录；创建 `/etc/stacknest-backup.conf`：

```dotenv
STACKNEST_BACKUP_COMPOSE=/opt/stacknest/current/server/compose.release.yaml
STACKNEST_BACKUP_ROOT=/opt/stacknest/backups
STACKNEST_COMPOSE_PROJECT=stacknest
STACKNEST_BACKUP_KEEP=14
```

```bash
chmod 600 /etc/stacknest-backup.conf
install -m 644 deploy/stacknest-backup.service deploy/stacknest-backup.timer /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now stacknest-backup.timer
systemctl list-timers stacknest-backup.timer
journalctl -u stacknest-backup.service --no-pager -n 50
```

任务按服务器时区每天 03:15 执行，随机延后最多 5 分钟。失败记录进入 journal；当前不自动发送告警邮件。定期把完整备份复制到服务器外，并安全保存环境配置、证书私钥、前后端版本和代理配置。自动本机备份不能替代异地副本。

## 7. 恢复

优先在隔离数据库和隔离文件目录演练。生产恢复会替换目标数据库及站点文件，必须先备份当前目标、停止 API 和所有入口，并使用同批数据库与文件备份。恢复需要匹配的应用版本及 PostgreSQL 17。

以下为维护窗口内的恢复示例；修改备份路径并确认 Compose 项目名正确后执行。若另有 `tunnel` 服务，也一并停止。

```bash
(
  set -euo pipefail
  stacknest_backup_dir=/opt/stacknest/backups/stacknest-YYYYMMDDTHHMMSSZ-PID
  (cd "$stacknest_backup_dir" && sha256sum -c SHA256SUMS)
  tar -tzf "$stacknest_backup_dir/files.tar.gz" > /dev/null

  docker compose stop nginx stacknest
  docker compose up -d --wait postgres
  docker compose exec -T postgres pg_restore -U stacknest -d stacknest --clean --if-exists --no-owner --no-privileges --single-transaction < "$stacknest_backup_dir/postgres.dump"
  docker compose run --rm --no-deps -T --entrypoint sh stacknest -ec 'rm -rf /data/sites /data/trash /data/pending /data/logs /data/previous /data/changes; tar -C /data -xzf -' < "$stacknest_backup_dir/files.tar.gz"
  docker compose rm -f migrate
  docker compose up -d
)
```

数据库恢复报错时事务回滚；数据库与文件恢复不是共同事务。任一步失败都保持应用停止，排除原因后从同批备份重新恢复，不能让应用在二者不匹配时运行。恢复完成后检查项目查询、站点文件与访问、删除和重启恢复。

## 7.1 当前服务器的发布与 SSH 入口

当前安装使用 `/opt/stacknest/releases/<release>/server`，环境文件和证书位于 `/opt/stacknest/shared/`，Compose 项目名固定 `stacknest`。`compose.release.yaml` 使用已构建的 `stacknest-app/web/migrate:${STACKNEST_RELEASE}` 镜像，等同于联合部署加上 SSH 辅助入口，不负责构建源码。普通服务器直接使用 `compose.yaml` 的构建流程即可。

升级前先生成三张匹配镜像、检查新目录的 Compose 和 Nginx 配置，再备份，更新私有环境文件中的发布号并启动新目录。保留旧发布目录及环境文件副本用于回退；有数据库迁移时不能只回退镜像。

`tunnel` 只监听服务器 `127.0.0.1:18080`。本机运行：

```bash
ssh -N -L 18080:127.0.0.1:18080 root@39.108.130.156
```

访问 `http://localhost:18080/`，项目链接为 `http://stack-nest-{slug}.localhost:18080/`，复用同一 SSH 隧道并隔离后台来源。模板为 `deploy/nginx/tunnel.conf`，保留带端口的 Host。这个本地 HTTP 入口显示 HTTP/1.1 属于正常情况，公网 HTTPS 入口另行支持 HTTP/2。不要向公网开放 18080。备案完成前使用此入口，公网验收另行进行。

## 8. 升级

更新前按第 6 节备份，并记录旧的前后端版本。准备匹配的新版本后，先构建镜像，再在维护窗口停掉旧 API 和入口，避免旧应用在数据库迁移期间继续写入：

```bash
docker compose build
```

构建成功后执行：

```bash
(
  set -euo pipefail
  docker compose stop nginx stacknest
  docker compose rm -f migrate
  docker compose up -d
  docker compose ps -a
)
```

删除的是已退出的一次性 migration 容器，不是数据卷；新容器会执行待应用的迁移。迁移失败时检查 `docker compose logs --tail=100 migrate`，保持应用停止，不要绕过迁移启动。依赖条件不等于零停机升级或运行期间自动修复，本流程会短暂停机。

升级后重新执行第 4 节的状态、API 和站点检查。仅重启容器不会重新构建代码。数据库迁移通常不能靠切回旧镜像撤销；需要回退时按第 7 节恢复匹配的版本、数据库和站点文件。仅前端改动可按 [Web 部署文档](https://github.com/zby909/stack-nest-web/blob/main/docs/DEPLOYMENT.md) 单独更新 nginx。

## 9. 常见问题与定位

| 现象                           | 检查方向                                                                                              |
| ------------------------------ | ----------------------------------------------------------------------------------------------------- |
| 构建找不到 `../web`            | 两个仓库是否并排，命令是否在 `server/` 执行                                                           |
| 80 端口已占用                  | 按第 5 节替换映射，由现有代理转发；不要同时占用宿主机 80                                              |
| `migrate` 退出非 0             | 查看迁移日志，检查 URL、密码、数据库健康状态和迁移文件；修复后重新执行升级启动步骤                    |
| 改 `.env` 密码仍连接失败       | 检查 shell 变量覆盖；已有数据卷的数据库密码不会随镜像初始化变量自动修改，须按数据库密码变更流程处理   |
| `/api/health` 返回 503       | 数据库连接不可用、2 秒探测超时或探测未配置；查看 `stacknest` 和 `postgres` 日志                        |
| `/api/health` 正常但列表失败   | 健康接口只验证数据库连接；继续检查迁移、表权限、登录状态与具体接口日志                                |
| 登录邮件没有到达               | 登录请求会对未知邮箱返回相同状态；检查邮箱名单、QQ SMTP 服务和授权码、发信日志及垃圾邮件箱            |
| 登录后写操作返回 403           | 浏览器应从同源页面调用 API；检查外层代理是否原样转发 Host 和真实请求协议                              |
| 页面或站点 404                 | 检查 DNS、Host、slug、站点目录及网关路由；上传前项目域名返回 404 是正常的                             |
| 上传返回 413 或 504            | 413 检查所有代理层的上传限制；504 检查 API 日志及内外层代理超时，前端 10 分钟超时不会自动延长代理超时 |
| 上传返回 507 / 503 | 507 表示磁盘达到 90% 或无法预留 412 MiB 处理预算；503 表示磁盘探测失败。查看存储卡和同请求编号日志 |
| 创建或启动提示数据目录权限错误 | 自定义挂载目录需允许容器 UID 10001 写入；默认 named volume 由镜像初始化，不要用全目录 777 处理        |
| 删除后 slug 仍提示占用         | 查看 `trash` 清理和启动恢复日志，排查存储问题后重启 API；不要直接删除暂存数据                         |
| 更新后偶发 502                 | 确认 Go 已启动；若曾单独重建 Go 容器导致 IP 改变，重启 nginx 使其重新解析后端                         |

```bash
docker compose ps -a
docker compose logs --tail=100 postgres migrate stacknest nginx
docker compose exec nginx nginx -t
```

数据库镜像初始化变量的行为见 [PostgreSQL 官方镜像说明](https://hub.docker.com/_/postgres)。本文命令与仓库配置保持一致；是否可在目标 Ubuntu 正常部署、备份及恢复，仍须完成 [上线验收](REQUIREMENTS.md)，不能仅凭本地构建通过认定。
