# StackNest Docker 部署与维护

本文用于 Linux 单实例部署。首次安装优先按 [版本镜像部署](INSTALL_RELEASE.md) 操作；需要构建源码时按 [源码 Docker 部署](GETTING_STARTED.md)；家用电脑使用 [Cloudflare Tunnel 独立教程](HOME_SERVER_TUNNEL.md)。平台核心始终是四个常驻容器：**Nginx、Go API、Runner、PostgreSQL**，另有执行完退出的 `migrate`。

所有命令均在 `server/` 目录、Ubuntu Bash 中执行。Web 构建与仅更新前端见 [Web 部署文档](https://github.com/zby909/stack-nest-web/blob/main/docs/DEPLOYMENT.md)；功能验收见 [需求文档](REQUIREMENTS.md)，代码开发见 [README](../README.md)。

## 1. 准备目录和运行环境

准备 Ubuntu 和可用的 Docker Engine，Compose **2.24.4+**。入口覆盖使用 `!override` 完整替换端口列表，防止 SSH / Tunnel 模式意外保留公网端口；见 [Docker 合并规则](https://docs.docker.com/reference/compose-file/merge/)。可选应用联网服务要求 Engine 28+。

默认配置包含 Runner，Docker 可用 Linux 内存至少 1600 MiB；建议 2 GiB 或以上，应用预算仍需给系统与平台留空间。

```text
workspace/
  server/  # 在这里运行本文命令
  web/     # 匹配版本，Compose 使用 ../web 构建 Nginx 镜像
```

```bash
docker version
docker compose version
```

镜像内完成 Go、Node.js 和 Atlas 构建，宿主无需安装开发工具或 PostgreSQL。磁盘需容纳数据库、站点、应用数据、镜像、上传临时文件及备份。只做代码开发时使用 README 中的 `compose.dev.yaml`，不使用生产入口。

## 2. 配置环境变量

首次创建；已有 `.env` 时保留并逐项检查：

```bash
if [ ! -f .env ]; then cp .env.compose.example .env; fi
chmod 600 .env
```

修改示例值，数据库密码在两处必须一致：

```dotenv
COMPOSE_PROJECT_NAME=stacknest
COMPOSE_FILE=compose.yaml:compose.tls.yaml
STACKNEST_ADMIN_DOMAIN=stack-nest.example.com
STACKNEST_BASE_DOMAIN=example.com
STACKNEST_TLS_CERT_DIR=./cert
STACKNEST_POSTGRES_PASSWORD=replace-with-long-random-password
STACKNEST_DATABASE_URL=postgres://stacknest:replace-with-long-random-password@postgres:5432/stacknest?sslmode=disable
STACKNEST_AUTH_ADMIN_EMAIL=admin@example.com
STACKNEST_AUTH_SUPER_ADMIN_EMAIL=
STACKNEST_AUTH_INITIAL_USER_EMAIL=
STACKNEST_AUTH_SECRET=replace-with-at-least-32-random-characters
STACKNEST_SMTP_HOST=smtp.qq.com
STACKNEST_SMTP_PORT=465
STACKNEST_SMTP_USER=your-qq-address@qq.com
STACKNEST_SMTP_PASSWORD=replace-with-new-qq-smtp-authorization-code
```

| 配置             | 要求                                                                                           |
| ---------------- | ---------------------------------------------------------------------------------------------- |
| 项目名           | 后续操作、升级、备份继续用同一名称，不能换名字误建空数据卷                                     |
| 管理域名         | 完整域名，不带协议、端口或路径                                                                 |
| 项目基础域名     | 不带 `*.`；静态项目使用 `stack-nest-{slug}.基础域名`                                           |
| 数据库密码       | 用 `openssl rand -hex 32` 生成；URL 使用 `postgres:5432`，不能填 localhost                     |
| 管理员邮箱       | 用户表为空时初始化；后续通过界面管理成员                                                       |
| 超管邮箱         | 留空沿用部署管理员；若指定其他邮箱，该邮箱必须已是管理员                                       |
| 初始普通用户邮箱 | 可留空，之后从界面添加                                                                         |
| 认证密钥         | 至少 32 字节随机值，可用 `openssl rand -base64 48` 生成                                        |
| SMTP             | 当前使用隐式 TLS，如 QQ 的 465；授权码不是邮箱登录密码                                         |
| 邀请登录地址     | 默认 `https://管理域名/login`；可用 `STACKNEST_INVITATION_LOGIN_URL` 设置固定 HTTPS 地址       |
| 证书目录         | 标准入口使用 `STACKNEST_TLS_CERT_DIR`；Cloudflare 普通代理模式另用 `STACKNEST_ORIGIN_CERT_DIR` |
| Docker 存储目录  | Runner 用于检查空间；自定义 Docker data-root 时设置 `STACKNEST_DOCKER_STORAGE_DIR`             |

URL 密码含 `@ : / #` 等字符时须百分号编码，含 `$` 时还要处理 Compose 插值；随机十六进制密码可避免这些问题。Compose 读取 `.env`，同名 shell 变量优先，见 [官方环境变量规则](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/)。

```bash
docker compose config --quiet
```

退出码 0 仅表示配置能解析，不能证明密码、证书或 DNS 正确。不要公开完整 `docker compose config` 输出、`.env` 或私钥。

## 3. 选择一个访问入口

`.env.compose.example` 默认选择**普通公网 HTTPS**。基础 `compose.yaml` 提供 HTTP，供外层代理或测试使用；入口覆盖按下面选择一种：

| 场景                       | `COMPOSE_FILE`                            | 发布的宿主端口            |
| -------------------------- | ----------------------------------------- | ------------------------- |
| 普通公网 HTTPS（默认）     | `compose.yaml:compose.tls.yaml`           | 80 重定向、443 HTTPS      |
| Cloudflare 普通橙云代理    | `compose.yaml:compose.cloudflare.yaml`    | 80 重定向、443 源站 HTTPS |
| 只通过 SSH 本地访问        | `compose.yaml:compose.ssh.yaml`           | 仅 `127.0.0.1:18080`      |
| 家用电脑 Cloudflare Tunnel | `compose.yaml:compose.tunnel-public.yaml` | 仅回环 18080、18081       |

这些覆盖修改**同一个 `nginx` 服务**，不新增第二个 Nginx。Go 8080、数据库 5432 和 Runner 控制入口均不发布宿主端口。

### 普通公网 HTTPS

DNS 管理域名及项目泛域名指向入口 IP。准备浏览器信任的证书，覆盖管理地址和 `*.项目基础域名`，在证书目录放完整链 `certificate.pem` 和配对私钥 `private.key`，权限分别 0644、0600，目录 0700。

`compose.tls.yaml` 启用 Nginx HTTPS 和 HTTP/2，不依赖 Cloudflare。平台不自动签发、续期证书；签发方式及续期 reload 见 [首次部署第 3 节](GETTING_STARTED.md#3-放置-https-证书)。云安全组及主机网络需允许 TCP 80、443。管理域名、静态站点、预览和应用均使用 HTTPS。

### Cloudflare 普通代理

与 Tunnel 不同，这种方式要求可达的公网源站。选择 `compose.cloudflare.yaml`，使用 `STACKNEST_ORIGIN_CERT_DIR`，Cloudflare 设为 **Full (strict)**。可使用 Cloudflare Origin CA 证书，浏览器使用边缘证书；只有边缘证书不足以完成加密回源，见 [Full (strict) 说明](https://developers.cloudflare.com/ssl/origin-configuration/ssl-modes/full-strict/)。

该模板只信任官方 Cloudflare 网段提供的访客 IP；网段快照需维护。普通直连 TLS 模板不信任客户端提交的 CF-Connecting-IP。不要启用 Flexible，不要对管理 API 设置缓存所有内容。

正式静态站点用 `Cache-Control: no-cache`，使用 Nginx 默认 ETag 与 Last-Modified 协商缓存；API、版本预览、Docker 应用和错误响应保留 `no-store`。Cloudflare 浏览器缓存 TTL 应尊重现有标头；旧覆盖缓存需清理，见 [Cloudflare 缓存说明](CLOUDFLARE_TUNNEL.md)。

### SSH 与家用 Tunnel

SSH 模式在自己的电脑执行：

```bash
ssh -N -L 18080:127.0.0.1:18080 用户@服务器
```

后台 `http://localhost:18080/`；静态站点 `http://stack-nest-{slug}.localhost:18080/`；应用 `http://stack-nest--app-{slug}.localhost:18080/`。它们共用同一端口，但使用不同 Host 隔离来源。本地 HTTP 显示 HTTP/1.1 正常。

Tunnel 连接器及控制台步骤见 [家用电脑独立教程](HOME_SERVER_TUNNEL.md)。回源端口 18081 只绑定回环，不对局域网或公网开放。无公网访问需求时不需要安装 cloudflared。

## 4. 首次启动与验证

```bash
docker compose up -d --build
docker compose ps -a
docker compose logs --tail=100 migrate stacknest runner nginx
docker compose exec -T nginx nginx -t
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
```

Compose 等待数据库健康、迁移成功、Runner 健康、API 健康，再启动 Nginx，见 [启动依赖说明](https://docs.docker.com/compose/how-tos/startup-order/)。

| 服务        | 预期状态         | 职责                                  |
| ----------- | ---------------- | ------------------------------------- |
| `postgres`  | Up / healthy     | 用户、项目、版本、应用等数据          |
| `migrate`   | Exited (0)       | 一次性应用镜像中的数据库迁移          |
| `runner`    | Up / healthy     | 通过私有 Unix socket 管理 Docker 应用 |
| `stacknest` | Up / healthy     | 邮箱认证、API、上传、预览及应用代理   |
| `nginx`     | Up，配置校验成功 | 前端、静态文件及请求分发              |

普通 HTTPS 模式检查：

```bash
curl -fsS https://stack-nest.example.com/api/health
curl -i https://stack-nest.example.com/api/projects
```

健康返回 `{"status":"ok"}`，未登录项目列表返回 401。HTTPS 证书、Host 和 DNS 都必须匹配；IP 首页返回 404 不代表部署失败。HTTP 80 入口会重定向，不应拿其 308 当 API 健康返回。

再完成 [首次部署第 5 节](GETTING_STARTED.md#5-完成一次实际部署)的邮箱登录、草稿预览、正式发布、项目资源访问和普通用户归属检查；Docker 镜像服务必须监听容器内 `0.0.0.0:实际端口`。自检不能替代这些实际操作。

## 5. 已有外部 HTTPS 反向代理

如果宿主或其他网关已经终结 TLS，使用基础 HTTP 模板，将 nginx 端口**替换**为 `127.0.0.1:18080:80`。不要同时选择 TLS、SSH 或 Tunnel 覆盖。可建本地 `compose.proxy.yaml`：

```yaml
services:
  nginx:
    ports: !override
      - "127.0.0.1:18080:80"
```

设置 `COMPOSE_FILE=compose.yaml:compose.proxy.yaml`。外层代理需保留原 Host、真实 HTTPS 协议、91 MiB 上传限制、WebSocket 和长请求；这是受信任代理链，内层端口不可公开。下面片段放入已有的 HTTPS server 中：

```nginx
client_max_body_size 91m;

location / {
    proxy_pass http://127.0.0.1:18080;
    proxy_http_version 1.1;
    proxy_request_buffering off;
    proxy_buffering off;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $stacknest_proxy_connection;
    proxy_read_timeout 600s;
    proxy_send_timeout 600s;
}
```

外层 Nginx 的 `http` 级别另定义：

```nginx
map $http_upgrade $stacknest_proxy_connection {
    default upgrade;
    "" close;
}
```

外层证书覆盖管理域名和项目泛域名；如果还经过其他 TLS 网关，按受信任代理链传递真实协议。该方案不要求 cloudflared，不修改已有外部代理的其他站点。

## 6. 日常维护

备份、升级、恢复、日志和排障统一见 [日常运维](OPERATIONS.md)。换服务器见 [完整迁移](MIGRATION.md)，硬配额和联网保护各按对应可选教程。

认证密钥还用于 Docker 配置加密，已有应用时不能直接更换；迁移保留原密钥，详见 [架构与维护边界](ARCHITECTURE.md)。

## 7.1 使用预构建镜像

优先下载完整版本部署包，按 [版本镜像部署](INSTALL_RELEASE.md) 启动。维护者通过 [GitHub Actions](RELEASING.md) 一起发布 API/Runner、Web/Nginx、迁移镜像，以及配套 Compose 和环境模板。

发布包使用 `compose.release.yaml`；模板已经填写 `STACKNEST_RELEASE` 和三个完整摘要：

| 变量                      | 用途                     |
| ------------------------- | ------------------------ |
| `STACKNEST_APP_IMAGE`     | API 与 Runner 共用的镜像 |
| `STACKNEST_WEB_IMAGE`     | 一个 Nginx，内含管理页面 |
| `STACKNEST_MIGRATE_IMAGE` | 数据库迁移               |

三个 IMAGE 格式为 `ghcr.io/所有者/stacknest-组件@sha256:摘要`，发布包已填写，首次安装无需手动拼接。普通 HTTPS 使用 `COMPOSE_FILE=compose.release.yaml:compose.tls.yaml`，家用 Tunnel 使用 `compose.release.yaml:compose.tunnel-public.yaml`。先 `docker compose pull`，再 `docker compose up -d`，不需要 `--build`。

升级必须整组更新三个 IMAGE，不能只修改版本号。离线交付时若未设置 IMAGE，仍兼容本地 `stacknest-app:<版本>`、`stacknest-web:<版本>`、`stacknest-migrate:<版本>` 镜像；通过 `docker load` 导入并设置 `STACKNEST_RELEASE` 即可。

源码和预构建模式的服务名称、数据卷和维护操作一致，升级继续使用原项目名。已有完全展开的私有运行配置需要按原方式更新，不能盲目覆盖，也不能假设它会读取新版环境变量。

## 8. 升级与验收

执行 [运维升级流程](OPERATIONS.md#4-升级)，同步 API、Runner、前端和数据库迁移，继续用原项目名。首次部署的 up 命令会执行全部迁移，不必照历史版本逐条执行 SQL。

统一验收标准见 [需求文档](REQUIREMENTS.md)，历史发布证据见 [检查档案](history/V1_REVIEW.md)；历史服务器地址不是新实例默认配置。
