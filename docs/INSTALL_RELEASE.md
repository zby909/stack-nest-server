# Docker 部署

使用已构建镜像安装 StackNest。Compose 会启动整套平台，服务器无需安装 Go 或 Node.js。[工作原理](https://github.com/zby909/stack-nest-server/blob/main/docs/ARCHITECTURE.md)另有说明。

准备 Linux amd64、Docker Engine **26+**、Compose **2.24.4+**、至少 2 GiB 内存，以及域名、SMTP 邮箱和 HTTPS 证书。启用可选应用联网保护需要 Engine **28+**。Docker 安装见 [官方说明](https://docs.docker.com/engine/install/ubuntu/)。

没有公网 IP？完成第 1、2 步，再接 [家用电脑教程](https://github.com/zby909/stack-nest-server/blob/main/docs/HOME_SERVER_TUNNEL.md)。

## 1. 下载

从 [Releases](https://github.com/zby909/stack-nest-server/releases) 下载同一版本的 `stacknest-…-linux-amd64.tar.gz` 和 `.sha256`。GitHub 的 Source code 压缩包是源码。

部署包、镜像和文档使用同一版本。旧包按包内 README 操作，主分支展示的新增功能不一定包含在旧版本中；预发布版本的限制见其 Release 说明。

在新目录执行，将 `vX.Y.Z` 换成实际版本：

```bash
sha256sum -c stacknest-vX.Y.Z-linux-amd64.tar.gz.sha256
tar -xzf stacknest-vX.Y.Z-linux-amd64.tar.gz
cd server
sha256sum -c SHA256SUMS
```

## 2. 配置

包内有 `bin/stacknest-setup` 时，在当前 `server/` 目录运行：

```bash
./bin/stacknest-setup
```

按提示填写域名、管理员邮箱和 SMTP 信息。向导自动生成数据库密码、连接串和认证密钥；SMTP 密码不回显，生成的 `.env` 仅当前用户可读写。已有 `.env` 时拒绝覆盖。

镜像摘要、版本和默认入口会保留。需要手动配置或修改入口时，查看 [配置参考](https://github.com/zby909/stack-nest-server/blob/main/docs/DEPLOYMENT.md)。

旧包没有向导时，复制 `.env.compose.example` 为 `.env`、设为 `chmod 600 .env`，再按 [手动配置](https://github.com/zby909/stack-nest-server/blob/main/docs/DEPLOYMENT.md#环境变量)填写域名、SMTP、随机密码和认证密钥，保留包内的镜像摘要。

用 `docker info --format '{{.DockerRootDir}}'` 查看 Docker 存储目录；若不是 `/var/lib/docker`，将实际路径填写到 `.env` 的 `STACKNEST_DOCKER_STORAGE_DIR`。

## 3. 域名与证书

将管理域名和 `*.example.com` 解析到服务器，开放入站 TCP 80、443。

在当前部署目录创建 `cert/`，放入覆盖这些域名的完整证书链与私钥：

```text
cert/certificate.pem
cert/private.key
```

```bash
chmod 700 cert
chmod 644 cert/certificate.pem
chmod 600 cert/private.key
```

证书申请、续期及 Cloudflare Origin CA 用法见 [证书配置](https://github.com/zby909/stack-nest-server/blob/main/docs/DEPLOYMENT.md#证书)。

## 4. 启动

```bash
docker compose config --quiet
docker compose pull
docker compose up -d --wait --wait-timeout 120
docker compose ps -a
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
```

四个常驻服务应运行，`migrate` 显示 `Exited (0)` 正常。

打开管理域名，用管理员邮箱获取验证码登录。上传一个静态项目，完成预览、发布和站点访问。

## 下一步

[使用平台](https://github.com/zby909/stack-nest-server/blob/main/docs/USAGE.md) · [Docker 应用](https://github.com/zby909/stack-nest-server/blob/main/docs/DOCKER_APPS.md) · [备份与升级](https://github.com/zby909/stack-nest-server/blob/main/docs/OPERATIONS.md) · [换服务器](https://github.com/zby909/stack-nest-server/blob/main/docs/MIGRATION.md)

按需启用 [XFS 配额](https://github.com/zby909/stack-nest-server/blob/main/docs/STORAGE_QUOTAS.md)或 [局域网保护](https://github.com/zby909/stack-nest-server/blob/main/docs/NETWORK_EGRESS.md)。
