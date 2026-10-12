# 使用版本镜像部署 StackNest

适合只想使用平台的个人开发者和小团队。下载发布包后，服务器只需 Docker，基础部署无需 Git、Go、Node.js，也无需下载前端源码。平台运行 Nginx、API、Runner、PostgreSQL 四个常驻容器；另有执行完退出的数据库迁移容器。

## 1. 下载部署包

在 [GitHub Releases](https://github.com/zby909/stack-nest-server/releases) 选择一个明确版本，下载同一版本的：

- `stacknest-vX.Y.Z-linux-amd64.tar.gz`
- `stacknest-vX.Y.Z-linux-amd64.tar.gz.sha256`

工作流成功发布后才会有这些文件。GitHub 自动生成的 **Source code** 压缩包不是部署包。当前版本面向 Linux x86_64 / amd64；暂不提供 ARM 镜像。

服务器准备 Docker Engine、Compose **2.24.4+**、至少 2 GiB 内存，以及可用的域名、SMTP 邮箱和 HTTPS 证书。使用 [Docker 官方安装说明](https://docs.docker.com/engine/install/ubuntu/)。可选应用联网功能要求 Docker Engine 28+。

在一个**新的目录**中校验并解压；将下面版本号替换为下载的实际版本：

```bash
sha256sum -c stacknest-vX.Y.Z-linux-amd64.tar.gz.sha256
tar -xzf stacknest-vX.Y.Z-linux-amd64.tar.gz
cd server
sha256sum -c SHA256SUMS
docker version
docker compose version
```

发布包包含三个镜像的完整摘要及前后端提交，记录在 `release.json`。镜像在启动时从 GHCR 拉取，不包含在压缩包里。公开镜像不需要登录；如果维护者还未将包设为公开，会出现拉取权限错误，需要维护者调整 GHCR 包可见性。

## 2. 填写配置

首次安装：

```bash
cp .env.compose.example .env
chmod 600 .env
openssl rand -hex 32
openssl rand -base64 48
nano .env
```

第一个随机值用于数据库密码，第二个用于认证密钥。填写：

| 配置                             | 内容                                               |
| -------------------------------- | -------------------------------------------------- |
| `COMPOSE_PROJECT_NAME`           | 默认 `stacknest`，升级保持不变                     |
| `COMPOSE_FILE`                   | 默认 `compose.release.yaml:compose.tls.yaml`       |
| `STACKNEST_ADMIN_DOMAIN`         | 管理地址，例如 `stack-nest.example.com`            |
| `STACKNEST_BASE_DOMAIN`          | 项目基础域名，例如 `example.com`，不带 `*.`        |
| `STACKNEST_POSTGRES_PASSWORD`    | 第一个随机值                                       |
| `STACKNEST_DATABASE_URL`         | 将密码替换为同一个随机值，保留 `postgres:5432`     |
| `STACKNEST_AUTH_ADMIN_EMAIL`     | 管理员登录邮箱                                     |
| `STACKNEST_AUTH_SECRET`          | 第二个随机值；已有实例必须保留                     |
| `STACKNEST_SMTP_HOST / PORT`     | SMTP 地址及隐式 TLS 端口，例如 `smtp.qq.com / 465` |
| `STACKNEST_SMTP_USER / PASSWORD` | 发件邮箱及 SMTP 授权码                             |
| `STACKNEST_TLS_CERT_DIR`         | 默认为 `./cert`                                    |

模板底部的 `STACKNEST_RELEASE` 和三个 `STACKNEST_*_IMAGE` 已由发布流程填写，首次安装直接保留。API 与 Runner 共用同一镜像。认证密钥还用于加密 Docker 应用配置；它不是可随意更换的临时密码。

## 3. 配置入口并启动

普通公网服务器：管理域名和 `*.example.com` 的 DNS 指向服务器，允许入站 TCP 80、443。把覆盖这些域名的完整证书链和私钥放到：

```text
server/cert/certificate.pem
server/cert/private.key
```

```bash
chmod 700 cert
chmod 644 cert/certificate.pem
chmod 600 cert/private.key
docker compose config --quiet
docker compose pull
docker compose up -d --wait --wait-timeout 120
docker compose ps -a
docker compose exec -T nginx nginx -t
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
```

`migrate` 显示 `Exited (0)` 正常；其他四个服务应运行。平台不自动申请或续期证书，按自己的证书工具配置续期；更换文件后先检查 Nginx 配置，再 reload。详见 [HTTPS 证书说明](https://github.com/zby909/stack-nest-server/blob/main/docs/GETTING_STARTED.md#3-放置-https-证书)。

访问 `https://stack-nest.example.com/`，使用管理员邮箱接收验证码登录。再上传一个静态项目，验证预览、发布及资源访问。`doctor` 不发送邮件，不能替代实际邮箱登录验证。

没有公网 IP 的家用电脑：改为 `COMPOSE_FILE=compose.release.yaml:compose.tunnel-public.yaml`，无需本机证书，按 [Cloudflare Tunnel 教程](https://github.com/zby909/stack-nest-server/blob/main/docs/HOME_SERVER_TUNNEL.md) 配置连接器；服务 URL 为 `http://127.0.0.1:18081`。临时 SSH 验收可使用 `compose.release.yaml:compose.ssh.yaml`，然后转发本机 18080。

## 4. 可选能力

- **应用磁盘硬配额**：需要 XFS、prjquota 和传统 overlay2，并叠加 `compose.quota.yaml`，见 [配额说明](https://github.com/zby909/stack-nest-server/blob/main/docs/STORAGE_QUOTAS.md)。
- **Docker 应用访问公网**：默认使用独立 internal 网络。发布包附带 `bin/stacknest-egress`，按 [联网保护](https://github.com/zby909/stack-nest-server/blob/main/docs/NETWORK_EGRESS.md) 安装宿主服务，再叠加 `compose.egress.yaml`；不用在服务器编译 Go。
- **备份和迁移**：`deploy/` 包含维护脚本，需 Python 3、Bash 等系统工具，见 [日常运维](https://github.com/zby909/stack-nest-server/blob/main/docs/OPERATIONS.md) 和 [迁移说明](https://github.com/zby909/stack-nest-server/blob/main/docs/MIGRATION.md)。

入口覆盖选择一种，可选功能在后面追加，例如 `compose.release.yaml:compose.tls.yaml:compose.quota.yaml`。

## 5. 升级已有实例

不要把新包直接解压覆盖正在使用的目录。先在旁边解压、校验，阅读版本说明，并按运维文档备份当前实例。

升级时保留项目名、原 `.env` 的密码和认证密钥、证书、已有自定义入口及配额配置。将新包的 Compose 和 `deploy/` 文件同步到原部署目录；对照新模板，仅更新版本及三个 `STACKNEST_*_IMAGE` 值，其他新增配置逐项合并。旧配置中的额外覆盖文件也要保留。

只修改 `STACKNEST_RELEASE` 不会替换已固定的镜像摘要；必须整组更新三个 IMAGE。先拉取，拉取失败时旧实例继续运行：

```bash
docker compose config --quiet
docker compose pull
```

全部拉取完成且备份验证通过后，在维护窗口执行：

```bash
(
  set -euo pipefail
  docker compose stop nginx stacknest runner
  docker compose rm -f migrate
  docker compose up -d --wait --wait-timeout 120
  docker compose exec -T nginx nginx -t
  docker compose exec -T stacknest /usr/local/bin/stacknest doctor
)
```

保留原数据卷，不执行 `down -v`。迁移失败时保持 API 停止并排查。升级会短暂停机；换回旧镜像不会自动恢复旧数据库或用户应用数据。旧实例若使用完全展开的私有 `compose.json`，应按原部署方式更新镜像和配置，不能仅改一个未被读取的 `.env`。详细流程见 [日常运维](https://github.com/zby909/stack-nest-server/blob/main/docs/OPERATIONS.md#4-升级)。
