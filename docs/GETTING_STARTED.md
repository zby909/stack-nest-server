# 从源码部署

需要修改平台代码时使用。直接使用平台，优先选择 [版本镜像部署](INSTALL_RELEASE.md)。

## 1. 获取代码

准备 Docker Compose 2.24.4+；两个仓库使用配套版本，放在相邻目录：

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

按 [安装教程第 2、3 步](INSTALL_RELEASE.md#2-配置)填写域名、邮箱、密码和密钥，并放好证书。

源码模板使用 `COMPOSE_FILE=compose.yaml:compose.tls.yaml`。其他入口见 [配置参考](DEPLOYMENT.md#访问入口)，家用电脑见 [Tunnel 教程](HOME_SERVER_TUNNEL.md)。

## 3. 构建并启动

```bash
docker compose config --quiet
docker compose up -d --build --wait --wait-timeout 120
docker compose ps -a
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
```

Go、前端和 Atlas 在构建容器中处理。宿主无需安装开发工具。

打开管理域名，完成邮箱登录和一次静态发布。[升级与备份](OPERATIONS.md) · [本地开发](DEVELOPMENT.md)
