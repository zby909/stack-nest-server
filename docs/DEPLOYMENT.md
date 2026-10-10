# StackNest 后端与联合部署

本文是 **Ubuntu + Docker Compose 单实例部署**的操作入口，涵盖 Web、Go API、PostgreSQL、迁移和 Nginx。所有命令均在 `server/` 目录、Ubuntu Bash 中执行；备份文件的重定向命令不要直接复制到 Windows PowerShell。

- 功能范围与统一验收标准：[需求文档](REQUIREMENTS.md)。
- 前端构建、API 地址及仅更新前端：[Web 部署文档](https://github.com/zby909/stack-nest-web/blob/main/docs/DEPLOYMENT.md)。
- 本地 Go 开发、代码生成和测试：[Server README](../README.md)。

## 1. 准备目录和运行环境

准备 Ubuntu 22.04 / 24.04 / 26.04、可用的 Docker Engine 与 Compose v2。镜像内完成 Go、Node.js 和 Atlas 构建，宿主机无需安装这些开发工具。磁盘需容纳数据库、所有站点、上传临时文件、镜像及备份，按实际项目量预留空间。

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

| 配置                 | 要求                                                                                                                                       |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| 管理域名             | 完整域名，不带协议、端口或路径                                                                                                             |
| 项目基础域名         | 不带 `*.`；项目地址为 `stack-nest-{slug}.基础域名`；管理域名应与项目域名区分                                                               |
| 源站证书目录         | 仅 Cloudflare Full (strict) 入口需要；放匹配的 `certificate.pem` 和 `private.key`                                                          |
| PostgreSQL 密码      | 必须替换示例密码；可用 `openssl rand -hex 32` 生成随机值                                                                                   |
| 数据库 URL           | 容器内使用 `postgres:5432`，不能填 `localhost`；密码必须与上一项一致                                                                       |
| 管理员与初始用户邮箱 | 首次启动且用户表为空时导入；之后通过管理员界面增删普通用户，管理员邮箱不能在界面删除                                                       |
| 认证密钥             | 至少 32 字节随机值，可用 `openssl rand -base64 48` 生成；用于验证码摘要                                                                    |
| QQ SMTP              | 发件邮箱完整地址，465 端口使用隐式 TLS；密码填新生成的 SMTP 授权码，不填 QQ 登录密码                                                       |
| 邀请登录地址         | 默认使用 `https://STACKNEST_ADMIN_DOMAIN/login`；可用 `STACKNEST_INVITATION_LOGIN_URL` 覆盖为固定 HTTPS 地址，不能包含凭据、查询参数或片段 |

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

Cloudflare 的[请求上传限制](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/4xx-client-error/error-413/)还受套餐及站点设置影响。本项目统一限制归档为 90 MiB，Nginx 请求体为 91 MiB，为表单开销预留余量；边缘限制更低时仍可能返回 413。不要对管理域名设置“缓存所有内容”。正式静态站点的 HTML 与资源使用 `Cache-Control: no-cache`，沿用 Nginx 默认 ETag（修改时间＋大小）与 Last-Modified 做协商缓存；管理 API、版本预览、Docker 应用和错误响应继续使用 `no-store`。不增加文件 hash 或发布版本判断；若已有覆盖源站缓存头的 Cloudflare 规则，应移除覆盖，并清理旧缓存。模板只信任官方 Cloudflare 网段传来的访客 IP，后端据此限流；网段快照需随官方变化维护。

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

管理员在邮箱页面添加普通用户时可选“发送邀请邮件”，默认不勾选。邀请复用验证码的 SMTP 服务，发信最多等待 20 秒；添加结果与发送结果分别显示。邀请未能确认发送时，邮箱已保留，可直接手动告知登录地址；重复添加不会重发邀请。界面显示邮件将使用的正式登录地址，SSH 入口不会被写进邀请。备案未通过时，此正式地址仍可能不可访问，发送前应确认收件人的访问方式。当前不提供后台重试队列或单独重发邀请接口。

`postgres-data` 保存数据库；`stacknest-data` 保存 `/data/sites` 当前站点、`previous` 上一版、`drafts` 待发布版、`changes` 更新/回退事务、`trash` 删除暂存、`pending` 上传恢复标记、`logs` 操作日志和 `tmp` 临时文件。Go 可写，项目 Nginx 只读，正式站点读取 `sites/{slug}`，版本预览主机代理到 Go 按数据库保留状态打开对应目录。实际卷名带 Compose 项目前缀，升级必须继续使用原项目名。`docker compose down -v` 会删除数据卷，不作为日常操作。

项目所有者由数据库的用户 ID 外键记录，站点目录名只是全局唯一 slug。升级到含 `project_owner` 迁移的版本前，先做第 6 节备份，再停止旧 API、应用迁移并启动新 API；不能让旧版 API 在迁移后继续接收上传。迁移把现有项目分配给管理员。删除普通用户会将其项目转交管理员，并使该用户会话失效；不会删除站点文件。

上传、更新、回退与删除都留下恢复依据，启动时结合数据库确认结果；数据库无法确认时保留现场并停止启动，不直接删文件。单个 API 实例独占写入数据卷。不要手动清理 `pending`、`changes` 或 `trash` 来释放 slug。更新和回退保留原地址，仅保留当前、上一正式版，另允许一个独立草稿；文件先完成校验和预览确认再切换，切换期间可能有极短的请求失败窗口。旧版服务不理解 `changes`，存在未完成事务时不可直接降级。

管理页可查询服务器保留的上传、更新、回退、删除、编辑、导出、暂停与恢复日志，按项目、操作、结果、邮箱、日期和关键词筛选后分页，普通用户总数只统计其可见记录。管理员还可在 `/system` 查看 CPU、内存和数据卷空间；普通用户访问 `/api/system` 会收到 403，`/api/storage` 只返回上传策略，不返回磁盘容量。`/data/logs/activity.jsonl` 每份约 1 MiB，保留当前文件和两份轮转文件；删除项目不会删除日志。请求日志为 JSON，记录状态码、耗时和请求编号，Docker 使用 10 MiB × 3 轮转。日志不含验证码、Cookie 或 SMTP 授权码。

使用仓库脚本备份，宿主机需要 Bash、Python 3、Docker Compose、flock 和 tar。参数是实际 Compose 文件、备份根目录，以及可选的摘要目录：

```bash
bash deploy/backup.sh compose.yaml /opt/stacknest/backups /opt/stacknest/shared/backup-status
```

如果原服务通过 `-p stacknest` 启动，调用脚本时也设置 `STACKNEST_COMPOSE_PROJECT=stacknest`；不要换项目名。脚本短暂停止 API，保存同一批 `postgres.dump`、`files.tar.gz`（sites/trash/pending/logs/previous/changes/paused/drafts），检查可读性并写入 SHA256SUMS，退出时恢复原本运行的 API。脚本默认保留最新 14 份完整备份，可用 `STACKNEST_BACKUP_KEEP` 调整；失败现场以 `.partial-` 开头，不作为有效备份，不自动删除。已有静态站点在备份期间仍由 Nginx 提供。

每日备份可使用 `deploy/stacknest-backup.service` 和 `.timer`。示例要求 `/opt/stacknest/current` 指向当前发布目录；创建 `/etc/stacknest-backup.conf`：

```dotenv
STACKNEST_BACKUP_COMPOSE=/opt/stacknest/current/server/compose.release.yaml
STACKNEST_BACKUP_ROOT=/opt/stacknest/backups
STACKNEST_BACKUP_STATUS_DIR=/opt/stacknest/shared/backup-status
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

任务按服务器时区每天 03:15 执行，随机延后最多 5 分钟。失败记录进入 journal；当前不自动发送告警邮件。本项目保留本机自动备份，换服务器时用 [完整迁移工具](MIGRATION.md) 携带数据库、文件、镜像与配置。本机备份仍随原服务器存储存在，到期前应完成迁移。

管理员可在服务器状态页查看备份结果。生产 `.env` 中的 `STACKNEST_BACKUP_STATUS_DIR` 必须与脚本第三参数及上面的 systemd 配置相同；发布目录会轮换时使用稳定的绝对路径。未指定第三参数或环境变量时，脚本写入 Compose 文件所在目录的 `backup-status/`，对应 Compose 默认挂载。修改 `.env` 后须重新创建 API 容器使挂载生效，按现有升级流程操作。

脚本通过原子替换写入 `status.json`，仅含时间、固定阶段名、结果和体积。摘要目录 0755、文件 0644，供容器 UID 10001 只读；备份原文件仍为私有权限。API 挂载此目录到 `/run/stacknest-backup`，不会挂载完整备份根目录或 Docker socket。本地 Go 如需读取，设置 `STACKNEST_BACKUP_STATUS_FILE` 为摘要文件的完整路径。

失败摘要保留最近完整备份的信息；损坏或不可读取返回 503，缺失则显示尚无记录。运行摘要超过 45 分钟提示可能中断，超过 48 小时没有新成功备份提示检查任务。页面状态不能代替 SHA256SUMS 校验及恢复演练。

API 0.5.0 增加项目展示信息编辑、部署 ZIP 导出和备份状态，没有新增数据库迁移。导出只包含发布文件，临时 ZIP 位于私有 `/data/exports`，连接结束或启动恢复时清理，不纳入备份。与上传一样遵守磁盘 90% 阈值，最多预留 216 MiB；同实例仅一个导出，客户端下载可取消。编辑和导出也记录日志。API 0.6.0 将日志改为服务端查询与分页，旧记录是否可查取决于轮转保留范围。

API 0.5.1 将 HTTPS 会话 Cookie 改为 `__Host-stacknest_session`，原 HTTPS 登录需要重新验证一次；本地 HTTP/SSH 入口不受影响。代理须继续覆盖 `X-Forwarded-Proto`，不能放行客户端伪造的协议。ZIP 索引解析增加 8 MiB 读取预算，过大的目录注释应移除后重新打包；90 MiB 归档、200 MiB 解压及 5000 条目限制继续有效。

API 0.6.0 增加站点暂停/恢复，无数据库迁移。必须同时更新 Go API、Web 和两份 Nginx 模板；暂停标记位于 `/data/paused/<slug>`，应与站点卷一起挂载和备份。暂停的页面和资源返回 503，更新、回退不解除暂停，启动时依据数据库重建标记；不要人工删除标记开放站点。

在 Linux 可运行 `python3 deploy/test_backup.py` 验证成功、数据库失败、API 恢复失败、摘要与轮转控制流。测试使用临时目录和假 Docker，不接触真实容器、数据库或备份；它不能替代真实恢复演练。

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
  docker compose run --rm --no-deps -T --entrypoint sh stacknest -ec 'rm -rf /data/sites /data/trash /data/pending /data/logs /data/previous /data/changes /data/paused /data/drafts; tar -C /data -xzf -' < "$stacknest_backup_dir/files.tar.gz"
  docker compose rm -f migrate
  docker compose up -d
)
```

数据库恢复报错时事务回滚；数据库与文件恢复不是共同事务。任一步失败都保持应用停止，排除原因后从同批备份重新恢复，不能让应用在二者不匹配时运行。恢复完成后检查项目查询、站点文件与访问、删除和重启恢复。

## API 0.9.0 的升级与预览

同步部署 Go、Web 和两份 Nginx 模板，并先应用 `20261009090000_release_notes.sql`，为版本增加更新说明。历史项目保持现有正式地址、归属和内容；以后从管理端上传先进入待发布状态，预览后确认发布。

版本 URL 为 `stack-nest--<UUID>.<项目基础域名>`。它仍是单级子域名，沿用泛 DNS 与通配证书；Nginx 将这个预留命名空间交给 Go，普通正式站点继续读取共享目录。SSH 入口使用 `stack-nest--<UUID>.localhost:18080`，无需修改本机 hosts。预览持有地址即可访问，不单独要求邮箱登录；暂停项目会关闭全部版本预览。

验收时新建临时项目，检查草稿仅在独立地址可见、明确发布后正式站点才更新、最近两版轮转、旧预览失效及暂停全部预览。按需比较版本文件，验证相同大小的内容修改能被识别。草稿位于 `/data/drafts`，新版备份和完整迁移均包含它；未完成的 `uploads` 仍不包含。

升级前保留配套备份。新增说明列本身兼容旧查询，但 0.8 不理解草稿状态及草稿发布恢复；创建草稿后不能仅切回旧镜像，应使用同批数据库、文件与旧镜像恢复。

## 7.1 当前服务器的运行配置与 SSH 入口

2026-10-10 已迁至 `zby909@100.68.110.44` 的 Ubuntu 虚拟机，通过 Tailscale 内网访问。Compose 项目名 `stacknest`，运行配置 `/data/stacknest/runtime/compose.json`，公网备用配置 `compose.public.json`，证书位于同目录 `cert/`。配置包含凭据，不输出完整内容，也不纳入 Git。

```bash
sudo docker compose -p stacknest -f /data/stacknest/runtime/compose.json ps
sudo docker compose -p stacknest -f /data/stacknest/runtime/compose.json logs --tail=50 stacknest
```

Docker 位于 `/data/docker`，应用可写层及持久化目录分别为 1 GiB 硬上限，见 [配额说明](STORAGE_QUOTAS.md)。每日备份在 `/data/stacknest/backups`，北京时间 03:15 运行，保留 14 份。

`tunnel` 只监听服务器 `127.0.0.1:18080`，本机执行：

```bash
ssh -N -L 18080:127.0.0.1:18080 zby909@100.68.110.44
```

访问 `http://localhost:18080/`，静态站点用 `http://stack-nest-{slug}.localhost:18080/`，Docker 应用用 `http://stack-nest--app-{slug}.localhost:18080/`，复用隧道并隔离后台与应用来源。本地 HTTP 显示 HTTP/1.1 正常，公网 HTTPS 入口另行支持 HTTP/2。

公网 DNS 未切换，也未开放公网 18080。Tailscale 的 100.x 地址只供内网使用；公网发布需另行确定可达入口，再验收 DNS、HTTPS 和备案。旧 ECS 数据保留，后台及应用冻结，见 [迁移与回退说明](MIGRATION.md)。

原发布布局 `/opt/stacknest/releases/<release>/server` 使用预构建的 `stacknest-app/web/migrate` 镜像。后续升级先构建匹配镜像、备份，再更新当前 Compose 配置；修改旧 ECS 环境文件不会影响新实例。普通新部署可使用本文 `compose.yaml` 构建流程。

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

API 0.8.0 的续传记录保存在应用数据卷 `uploads/` 中，普通重启不会清空；未完成上传固定保留 24 小时，启动和每小时清理。发布后只保留小体积确认记录，临时原包会删除。备份/迁移包含正式版本、已完成上传的待发布版和必要恢复信息，不包含这些临时上传；迁移后需重新选择未完成文件。当前续传锁为单实例设计，不要并行启动多个 API 实例共享同一数据卷。管理页面应通过 HTTPS 或 localhost 访问，否则浏览器可能不提供文件摘要所需的 Web Crypto。

| 现象                           | 检查方向                                                                                              |
| ------------------------------ | ----------------------------------------------------------------------------------------------------- |
| 构建找不到 `../web`            | 两个仓库是否并排，命令是否在 `server/` 执行                                                           |
| 80 端口已占用                  | 按第 5 节替换映射，由现有代理转发；不要同时占用宿主机 80                                              |
| `migrate` 退出非 0             | 查看迁移日志，检查 URL、密码、数据库健康状态和迁移文件；修复后重新执行升级启动步骤                    |
| 改 `.env` 密码仍连接失败       | 检查 shell 变量覆盖；已有数据卷的数据库密码不会随镜像初始化变量自动修改，须按数据库密码变更流程处理   |
| `/api/health` 返回 503         | 数据库连接不可用、2 秒探测超时或探测未配置；查看 `stacknest` 和 `postgres` 日志                       |
| `/api/health` 正常但列表失败   | 健康接口只验证数据库连接；继续检查迁移、表权限、登录状态与具体接口日志                                |
| 登录邮件没有到达               | 登录请求会对未知邮箱返回相同状态；检查邮箱名单、QQ SMTP 服务和授权码、发信日志及垃圾邮件箱            |
| 登录后写操作返回 403           | 浏览器应从同源页面调用 API；检查外层代理是否原样转发 Host 和真实请求协议                              |
| 页面或站点 404                 | 检查 DNS、Host、slug、站点目录及网关路由；上传前项目域名返回 404 是正常的                             |
| 上传返回 413 或 504            | 413 检查所有代理层的上传限制；504 检查 API 日志及内外层代理超时，前端 10 分钟超时不会自动延长代理超时 |
| 上传返回 507 / 503             | 507 表示磁盘达到 90% 或无法预留 412 MiB 处理预算；503 表示磁盘探测失败。查看存储卡和同请求编号日志    |
| 创建或启动提示数据目录权限错误 | 自定义挂载目录需允许容器 UID 10001 写入；默认 named volume 由镜像初始化，不要用全目录 777 处理        |
| 删除后 slug 仍提示占用         | 查看 `trash` 清理和启动恢复日志，排查存储问题后重启 API；不要直接删除暂存数据                         |
| 更新后偶发 502                 | 确认 Go 已启动；若曾单独重建 Go 容器导致 IP 改变，重启 nginx 使其重新解析后端                         |

```bash
docker compose ps -a
docker compose logs --tail=100 postgres migrate stacknest nginx
docker compose exec nginx nginx -t
```

数据库镜像初始化变量的行为见 [PostgreSQL 官方镜像说明](https://hub.docker.com/_/postgres)。本文命令与仓库配置保持一致；是否可在目标 Ubuntu 正常部署、备份及恢复，仍须完成 [上线验收](REQUIREMENTS.md)，不能仅凭本地构建通过认定。

## Docker 应用运行器

首版按仓库镜像地址部署单个 HTTP 服务，配置、权限与限制见 [Docker 应用说明](DOCKER_APPS.md)。Compose 的 runner 使用同一个 Go 镜像，以独立 root 进程持有 Docker socket，后台仅挂载私有控制卷。运行器不监听公网端口；用户应用只挂载自己的数据目录。若 Docker 存储根目录不是 /var/lib/docker，请设置 STACKNEST_DOCKER_STORAGE_DIR。

部署时应用 20261009130000_container_apps.sql 并同步 Go、Web、Nginx 和备份迁移工具。应用域名 stack-nest--app-<slug> 使用原有泛域名和证书；SSH 使用 stack-nest--app-<slug>.localhost:18080。应用暂时不能主动访问外网，总内存配额 768 MiB / 最多 3 个应用，适用于当前小服务器。公网访问仍需等待备案及入口验收。
