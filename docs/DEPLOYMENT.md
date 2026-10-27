# 配置参考

首次安装见 [Docker 部署](INSTALL_RELEASE.md)。本页用于查询选项，工作原理见 [架构](ARCHITECTURE.md)。

## 环境变量

Compose 使用部署目录的 `.env`，同名 shell 环境变量优先。[完整模板](../.env.compose.example)包含默认值。

发布包提供 `./bin/stacknest-setup` 交互向导，源码目录可运行 `go run ./cmd/stacknest-setup`。它只为新安装创建 `.env`，自动生成相互匹配的数据库密码、连接串和认证密钥，不覆盖已有文件，也不安装或启动服务。

手动配置时，复制 `.env.compose.example` 为 `.env` 并设权限 `chmod 600 .env`；用 `openssl rand -hex 32` 生成数据库密码，`openssl rand -base64 48` 生成认证密钥，再填写下表。向导生成的文件遵循 **Compose `.env` 格式**，不要用 shell 的 `source` 加载。

数据库密码同时填写到 `STACKNEST_POSTGRES_PASSWORD` 和连接串；保留原模板的入口、版本和镜像摘要。用 `docker info --format '{{.DockerRootDir}}'` 核对 Docker 实际存储路径，非默认目录需同步填写到 `STACKNEST_DOCKER_STORAGE_DIR`。

| 配置                                | 说明                                             |
| ----------------------------------- | ------------------------------------------------ |
| `COMPOSE_PROJECT_NAME`              | 默认 `stacknest`，升级和备份保持一致             |
| `COMPOSE_FILE`                      | 基础文件加一种入口，可再叠加配额或联网文件       |
| `STACKNEST_ADMIN_DOMAIN`            | 管理域名，不带协议、端口或路径                   |
| `STACKNEST_BASE_DOMAIN`             | 项目基础域名，不带 `*.`                          |
| `STACKNEST_POSTGRES_PASSWORD`       | 与 `STACKNEST_DATABASE_URL` 的密码一致           |
| `STACKNEST_DATABASE_URL`            | 容器内数据库地址使用 `postgres:5432`             |
| `STACKNEST_AUTH_ADMIN_EMAIL`        | 初始管理员邮箱                                   |
| `STACKNEST_AUTH_SUPER_ADMIN_EMAIL`  | 留空沿用初始管理员；指定时须为已有管理员         |
| `STACKNEST_AUTH_INITIAL_USER_EMAIL` | 可选初始普通成员                                 |
| `STACKNEST_AUTH_SECRET`             | 至少 32 字节随机密钥，升级和迁移保留原值         |
| `STACKNEST_SMTP_HOST / PORT`        | 隐式 TLS SMTP，默认 `smtp.qq.com:465`            |
| `STACKNEST_SMTP_USER / PASSWORD`    | 发件邮箱及 SMTP 授权码                           |
| `STACKNEST_INVITATION_LOGIN_URL`    | 可选固定 HTTPS 邀请地址，默认管理域名的 `/login` |
| `STACKNEST_TLS_CERT_DIR`            | 标准 TLS 证书目录，默认 `./cert`                 |
| `STACKNEST_ORIGIN_CERT_DIR`         | Cloudflare 普通代理的源站证书目录                |
| `STACKNEST_BACKUP_STATUS_DIR`       | 备份摘要目录，须与备份脚本配置一致               |
| `STACKNEST_DOCKER_STORAGE_DIR`      | 实际 Docker data-root，默认 `/var/lib/docker`    |

发布包另外填写 `STACKNEST_RELEASE` 和三个 `STACKNEST_*_IMAGE`，升级时整组更新。自定义数据库密码须做 URL 编码；使用随机十六进制值可避免特殊字符。

用户表为空时初始化成员，后续在后台管理。认证密钥必须自行生成，空值、过短或旧模板示例值会阻止启动；它还用于 Docker 配置加密，不能直接替换已有实例的值。

## 访问入口

下表以发布包为例。源码部署将基础文件换成 `compose.yaml`。

| 场景                | COMPOSE_FILE                                      | 宿主端口          |
| ------------------- | ------------------------------------------------- | ----------------- |
| 普通 HTTPS          | `compose.release.yaml:compose.tls.yaml`           | 80、443           |
| Cloudflare 橙云代理 | `compose.release.yaml:compose.cloudflare.yaml`    | 80、443           |
| SSH 本地访问        | `compose.release.yaml:compose.ssh.yaml`           | 回环 18080        |
| 家用 Tunnel         | `compose.release.yaml:compose.tunnel-public.yaml` | 回环 18080、18081 |

只选择一种入口；配额和联网配置追加在后面。所有入口复用同一个 Nginx。

SSH 在自己的电脑执行：

```bash
ssh -N -L 18080:127.0.0.1:18080 用户@服务器
```

管理后台使用 `http://localhost:18080`，项目使用界面返回的 `.localhost:18080` 地址。

## 证书

标准 HTTPS 需要浏览器信任的完整证书链，覆盖管理域名和项目泛域名。可使用自己的 ACME 工具申请与续期，泛域名通常使用 DNS 验证。

证书目录包含 `certificate.pem` 和 `private.key`。平台不自动签发证书；续期后更新文件并执行：

```bash
docker compose exec -T nginx nginx -t
docker compose exec -T nginx nginx -s reload
```

Cloudflare 普通代理使用 `compose.cloudflare.yaml`、Origin 证书及 **Full (strict)**。Origin CA 证书只适合经过 Cloudflare 的访问。

Tunnel 使用本机 HTTP 回源，无需服务器证书，步骤见 [家庭部署](HOME_SERVER_TUNNEL.md)。缓存选项见 [Cloudflare 参考](CLOUDFLARE_TUNNEL.md#缓存)。

## 已有外部 HTTPS 代理

使用基础 HTTP 模板，建立本地 `compose.proxy.yaml`：

```yaml
services:
  nginx:
    ports: !override
      - "127.0.0.1:18080:80"
```

设置 `COMPOSE_FILE=compose.release.yaml:compose.proxy.yaml`。外层代理终结 TLS，保留 Host、真实 HTTPS 协议、WebSocket，并允许 91 MiB 请求和 600 秒超时。

外层 Nginx 的 `http` 级别：

```nginx
map $http_upgrade $stacknest_proxy_connection {
    default upgrade;
    "" close;
}
```

对应 HTTPS server 中：

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

内层端口保持回环绑定。[升级、备份与排障](OPERATIONS.md) · [磁盘配额](STORAGE_QUOTAS.md) · [应用联网](NETWORK_EGRESS.md)
