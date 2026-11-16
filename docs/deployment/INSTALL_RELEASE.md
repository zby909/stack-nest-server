# Docker 部署

Docker Compose 使用已构建镜像启动 StackNest。服务器无需安装 Go 或 Node.js。

准备 Linux amd64、Docker Engine **26+**、Compose **2.24.4+**、至少 2 GiB 内存，以及域名、SMTP 邮箱和 HTTPS 证书。启用可选应用联网保护需要 Engine **28+**。Docker 安装见 [官方说明](https://docs.docker.com/engine/install/ubuntu/)。

没有公网 IP 时，按 [家用电脑教程](https://github.com/zby909/stack-nest-server/blob/main/docs/deployment/HOME_SERVER_TUNNEL.md)选择 Tunnel 入口。

## 1. 下载

在 [Releases](https://github.com/zby909/stack-nest-server/releases) 的 Assets 选择平台部署包 `stacknest-vX.Y.Z-linux-amd64.tar.gz` 和同名校验文件 `.tar.gz.sha256`。用 AI 或 CI 更新业务项目时，见 [CLI 下载](https://github.com/zby909/stack-nest-server/blob/main/docs/usage/README.md#下载-cli)。

在新目录执行，将 `vX.Y.Z` 换成实际版本：

```bash
sha256sum -c stacknest-vX.Y.Z-linux-amd64.tar.gz.sha256
tar -xzf stacknest-vX.Y.Z-linux-amd64.tar.gz
cd server
sha256sum -c SHA256SUMS
```

## 2. 配置

在解压后的 `server/` 目录运行：

```bash
./bin/stacknest-setup
```

填写域名、管理员邮箱和 SMTP 信息。向导生成仅当前用户可读写的 `.env`，自动配置数据库密码和认证密钥。有现成配置时按 [配置参考](https://github.com/zby909/stack-nest-server/blob/main/docs/deployment/DEPLOYMENT.md)调整。

用 `docker info --format '{{.DockerRootDir}}'` 查看 Docker 存储目录；若不是 `/var/lib/docker`，将实际路径填写到 `.env` 的 `STACKNEST_DOCKER_STORAGE_DIR`。

## 3. 域名与证书

将管理域名和项目泛域名（例如 `stack-nest.example.com` 与 `*.example.com`）解析到服务器，开放入站 TCP 80、443。

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

证书申请、续期及 Cloudflare Origin CA 用法见 [证书配置](https://github.com/zby909/stack-nest-server/blob/main/docs/deployment/DEPLOYMENT.md#证书)。

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

[使用平台](https://github.com/zby909/stack-nest-server/blob/main/docs/usage/README.md) · [Docker 应用](https://github.com/zby909/stack-nest-server/blob/main/docs/usage/DOCKER_APPS.md) · [备份与升级](https://github.com/zby909/stack-nest-server/blob/main/docs/deployment/OPERATIONS.md) · [架构原理](https://github.com/zby909/stack-nest-server/blob/main/docs/architecture/README.md)

按需启用 [XFS 配额](https://github.com/zby909/stack-nest-server/blob/main/docs/deployment/STORAGE_QUOTAS.md)或 [局域网保护](https://github.com/zby909/stack-nest-server/blob/main/docs/deployment/NETWORK_EGRESS.md)。
