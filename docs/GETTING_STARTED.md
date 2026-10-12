# 从源码使用 Docker Compose 部署

已有发行版时，优先按 [版本镜像部署](INSTALL_RELEASE.md) 下载部署包，服务器不需要构建代码。本文保留源码构建步骤，适合开发者或需要自行修改平台的部署者。

StackNest 面向个人开发者和小团队的自有服务器。平台运行四个常驻容器：**Nginx、Go API、Runner、PostgreSQL**。另有一次性的 `migrate` 容器，执行数据库迁移后退出。前端在构建时打包进 Nginx 镜像；不需要单独的 Node 服务。

下面使用普通 Linux 公网服务器和 HTTPS。**家用电脑、CGNAT 或没有公网 IP**，改按 [家用电脑 + Cloudflare Tunnel 教程](HOME_SERVER_TUNNEL.md)，其余服务完全相同。

## 1. 准备服务器和代码

准备 Linux Docker Engine 与 **Docker Compose 2.24.4 或更新版本**；启用应用联网功能还需 Docker Engine 28+。入口覆盖文件使用 `!override` 替换端口列表，旧版 Compose 不能正确读取，见 [Docker 官方合并规则](https://docs.docker.com/reference/compose-file/merge/)。

默认配置包含 Runner，Docker 可用 Linux 内存至少 1600 MiB；建议 2 GiB 或以上，应用预算仍需给系统与平台留空间。

在 Ubuntu 安装 Docker 时使用 [Docker 官方安装步骤](https://docs.docker.com/engine/install/ubuntu/)，然后确认：

```bash
docker version
docker compose version
```

下文命令在 Ubuntu Bash 中执行，当前用户需要 Docker 操作权限。获取两个匹配版本的仓库，并排放置：

```bash
mkdir -p ~/stacknest
cd ~/stacknest
git clone https://github.com/zby909/stack-nest-server.git server
git clone https://github.com/zby909/stack-nest-web.git web
cd server
```

如果仓库尚未开放，需要仓库访问权限，或由维护者提供完整的 `server/` 和 `web/` 源码包。不能只下载后端。容器中完成 Go、Node.js 和 Atlas 构建，**宿主机无需安装这些开发工具或 PostgreSQL**。

配置 DNS，替换为自己的域名和服务器 IP：

| 名称                     | 类型 | 目标            |
| ------------------------ | ---- | --------------- |
| `stack-nest.example.com` | A    | 服务器公网 IPv4 |
| `*.example.com`          | A    | 同一 IPv4       |

仅在 IPv6 已可达时添加 AAAA。保留已有的其他明确子域名记录。开放入口 TCP 80、443；数据库、API 和 Runner 不发布宿主端口。

## 2. 配置邮箱和密钥

首次执行；已有配置不要覆盖：

```bash
if [ ! -f .env ]; then cp .env.compose.example .env; fi
chmod 600 .env
openssl rand -hex 32
openssl rand -base64 48
nano .env
```

第一个随机值用于数据库密码，第二个用于认证密钥。填写：

| 配置                             | 内容                                                     |
| -------------------------------- | -------------------------------------------------------- |
| `COMPOSE_PROJECT_NAME`           | 保留 `stacknest`，后续升级不变                           |
| `COMPOSE_FILE`                   | 默认 `compose.yaml:compose.tls.yaml`                     |
| `STACKNEST_ADMIN_DOMAIN`         | `stack-nest.example.com`                                 |
| `STACKNEST_BASE_DOMAIN`          | `example.com`，不带 `*.`                                 |
| `STACKNEST_POSTGRES_PASSWORD`    | 第一个随机值                                             |
| `STACKNEST_DATABASE_URL`         | 将其中的示例密码替换为同一个随机值，保留 `postgres:5432` |
| `STACKNEST_AUTH_ADMIN_EMAIL`     | 第一次登录的管理员邮箱                                   |
| `STACKNEST_AUTH_SECRET`          | 第二个随机值                                             |
| `STACKNEST_SMTP_HOST / PORT`     | SMTP 服务和隐式 TLS 端口，如 `smtp.qq.com / 465`         |
| `STACKNEST_SMTP_USER / PASSWORD` | 发件邮箱和 SMTP 授权码                                   |
| `STACKNEST_TLS_CERT_DIR`         | 证书目录，默认 `./cert`                                  |

管理员默认同时是超管，可以添加成员、配置实例资源预算。只有已有管理员才能被 `STACKNEST_AUTH_SUPER_ADMIN_EMAIL` 指定为超管；一般首次部署留空即可。初始普通用户可留空，登录后再从邮箱页面添加。SMTP 发件邮箱不必等于登录管理员邮箱。

认证密钥还用于 Docker 配置加密；已有应用后不要直接换值，迁移保留原密钥。

不提交 `.env`，不把 SMTP 授权码或数据库连接串贴到日志。完整配置解释见 [联合部署文档](DEPLOYMENT.md#2-配置环境变量)。

## 3. 放置 HTTPS 证书

证书必须覆盖管理域名和项目泛域名，例如 `*.example.com` 可以同时覆盖上述管理地址、静态项目、版本预览和 Docker 应用。直接供浏览器访问时，使用浏览器信任的 CA 证书；Cloudflare Origin CA 证书用于 Cloudflare 普通代理回源，不能作为浏览器直连证书。

使用自己的证书签发工具获取证书。Let's Encrypt 的通配证书需要 DNS-01 验证，不能只使用 HTTP-01，见 [官方验证方式](https://letsencrypt.org/docs/challenge-types/)。

将**完整证书链**和配对私钥保存为：

```text
server/cert/
  certificate.pem
  private.key
```

```bash
chmod 700 cert
chmod 644 cert/certificate.pem
chmod 600 cert/private.key
```

平台负责读取证书，**不负责自动签发和续期**；在自己的证书工具中配置续期与部署钩子。续期替换文件后执行 `docker compose exec -T nginx nginx -t`，通过再执行 `docker compose exec -T nginx nginx -s reload`。

## 4. 启动和自检

在 `server/` 执行，Compose 自动读取 `.env` 中选定的入口：

```bash
docker compose config --quiet
docker compose up -d --build
docker compose ps -a
docker compose exec -T nginx nginx -t
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
```

首次构建会下载依赖和基础镜像，需要可用的网络和足够磁盘。预期 `postgres`、`runner`、`stacknest` 健康，`nginx` 运行；`migrate` 为 `Exited (0)`。

`doctor` 检查数据库、认证配置、磁盘、Runner、SMTP TLS/认证及可选配额/联网服务。它不发送邮件，不验证公网 DNS 或应用业务。独立 Nginx 部署时“API 容器内未发现前端”提示正常；未启用硬配额和联网服务的提示也不代表基础部署失败。失败项先修复，再继续验收。

```bash
curl -fsS https://stack-nest.example.com/api/health
curl -i https://stack-nest.example.com/api/projects
```

健康应返回 `{"status":"ok"}`，未登录的项目列表应返回 401。不要将带密码的完整 `docker compose config` 输出公开；验证配置使用 `--quiet`。

## 5. 完成一次实际部署

1. 浏览器打开 `https://stack-nest.example.com/`，用管理员邮箱接收验证码并登录。
2. 上传含 `index.html` 的 ZIP 或未加密单卷 RAR，先打开待发布版预览，确认后正式发布。
3. 打开 `https://stack-nest-项目标识.example.com/`，确认 HTML、CSS 和图片正确；普通页面路径支持 SPA 回退，缺失资源应为 404。
4. 若需要 Docker 应用，准备已构建好的 Linux amd64 镜像地址，填写它实际监听的容器端口，部署一个小 HTTP 服务。平台不构建用户的 Dockerfile。应用必须监听 `0.0.0.0`，不能只监听容器内 `127.0.0.1`。
5. 按需添加普通用户。默认不发邀请，勾选并确认后才发送；再核对普通用户只管理自己的项目和应用。

## 6. 可选功能与维护

- **磁盘硬配额**：普通文件系统可以部署平台；Docker 可写层与应用数据的硬限额需要另行配置 XFS + prjquota + 传统 overlay2，并叠加 `compose.quota.yaml`。未配置时不能宣称已有限额，见 [配额说明](STORAGE_QUOTAS.md)。
- **应用访问外网**：默认每应用独立 internal 网络，不主动访问公网。需要联网时安装宿主 Go 规则服务并叠加 `compose.egress.yaml`；它允许公网和 Linux 本机服务、禁止直接访问其他局域网设备，见 [联网配置](NETWORK_EGRESS.md)。
- **预算**：默认全实例最多 3 个 Docker 应用、总计 768 MiB，单应用最多 1024 MiB。超管可在界面调整；停止应用仍占预留额度，不按每应用都分配 768 MiB。
- **备份与升级**：见 [日常运维](OPERATIONS.md)。换机器使用 [完整迁移](MIGRATION.md)，迁移包携带镜像、数据库和业务文件。
- **SSH 临时验收**：将 `COMPOSE_FILE` 改为 `compose.yaml:compose.ssh.yaml`，执行启动命令；在自己电脑运行 `ssh -N -L 18080:127.0.0.1:18080 用户@服务器`，打开 `http://localhost:18080/`。切回公网 HTTPS 前重新选择入口并更新容器。
- **家用电脑公网访问**：见 [Cloudflare Tunnel 独立教程](HOME_SERVER_TUNNEL.md)，不需要公网 IP 或路由器端口转发。

入口覆盖文件选择一种即可；添加配额或联网覆盖时保留当前入口，例如 `compose.yaml:compose.tls.yaml:compose.quota.yaml`。预构建镜像部署使用 `compose.release.yaml` 替换基础文件，详见 [部署文档](DEPLOYMENT.md#71-使用预构建镜像)。
