# 从源码部署

需要修改平台代码时使用。直接使用平台，优先选择 [版本镜像部署](INSTALL_RELEASE.md)。

## 1. 获取代码

准备 Linux amd64、Docker Engine 26+、Compose 2.24.4+；两个仓库使用配套版本，放在相邻目录：

```bash
mkdir stacknest
cd stacknest
git clone https://github.com/zby909/stack-nest-server.git server
git clone https://github.com/zby909/stack-nest-web.git web
cd server
```

部署发布版本时，在两个仓库检出同名标签。`main` 可能包含尚未发布的改动。

## 2. 配置入口

```bash
cp .env.compose.example .env
chmod 600 .env
```

按 [配置参考](DEPLOYMENT.md#环境变量)手动填写域名、SMTP、随机数据库密码和认证密钥。数据库密码与连接串中的密码保持一致；保留源码模板的 `COMPOSE_FILE`。证书放置方法见 [证书配置](DEPLOYMENT.md#证书)。

需要交互向导且宿主已有 Go 时，可在复制 `.env` 之前运行 `go run ./cmd/stacknest-setup`；向导拒绝覆盖已有 `.env`。源码目录不带发布包中的 `bin/stacknest-setup`。

用 `docker info --format '{{.DockerRootDir}}'` 核对实际 Docker 存储目录，并填写 `STACKNEST_DOCKER_STORAGE_DIR`。

源码模板使用 `COMPOSE_FILE=compose.yaml:compose.tls.yaml`。其他入口见 [配置参考](DEPLOYMENT.md#访问入口)，家用电脑见 [Tunnel 教程](HOME_SERVER_TUNNEL.md)。

## 3. 构建并启动

```bash
docker compose config --quiet
docker compose up -d --build --wait --wait-timeout 120
docker compose ps -a
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
```

Go、前端和 Atlas 在构建容器中处理。手动配置方案无需宿主开发工具；构建阶段还需要额外的可用内存与临时磁盘空间。

打开管理域名，完成邮箱登录和一次静态发布。[升级与备份](OPERATIONS.md) · [本地开发](DEVELOPMENT.md)
