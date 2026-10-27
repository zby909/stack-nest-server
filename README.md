# StackNest

在自己的服务器上管理静态站点和 Docker 应用。

面向个人开发者和小团队：上传、预览、发布、查看日志，以及换机迁移。

[开始部署](docs/INSTALL_RELEASE.md) · [下载版本](https://github.com/zby909/stack-nest-server/releases) · [文档](docs/README.md) · [Web 仓库](https://github.com/zby909/stack-nest-web)

![StackNest 管理界面](https://raw.githubusercontent.com/zby909/stack-nest-web/main/docs/images/projects-desktop.png)

## 功能

- **静态站点**：单 HTML / ZIP / RAR、断点续传、独立预览、确认发布和上一版回退。
- **Docker 应用**：镜像部署、环境变量、数据目录、启停和运行日志。
- **成员管理**：邮箱验证码登录，用户、管理员和超级管理员权限。
- **资源管理**：CPU、内存和上传容量限制，可选磁盘硬配额与局域网保护。
- **运维**：操作日志、服务器状态、备份和完整迁移。
- **界面**：手机布局、明暗主题。

成员管理保护后台操作权限；站点、应用和预览地址默认公开。

## 部署

推荐使用 [Docker 版本部署包](docs/INSTALL_RELEASE.md)：

1. 从 Releases 下载部署包和校验文件。
2. 填写域名、管理员邮箱及 SMTP 配置，准备 HTTPS 证书。
3. 执行 `docker compose pull` 和 `docker compose up -d --wait`。

部署完成后，上传 [最小示例站点](examples/README.md)体验预览与发布。

支持 Linux amd64，需 Docker Compose 2.24.4+、至少 2 GiB 内存。部署包含 Compose 和配置模板，无需在服务器编译代码。

没有公网 IP 的家用电脑，使用 [Cloudflare Tunnel 教程](docs/HOME_SERVER_TUNNEL.md)。

## 开发

后端使用 Go、PostgreSQL、Ent 和 Atlas；管理端使用 React、TypeScript、Vite 和 Tailwind CSS。

- [本地开发与测试](docs/DEVELOPMENT.md)
- [从源码构建部署](docs/GETTING_STARTED.md)
- [架构与工作原理](docs/ARCHITECTURE.md)
- [发布版本](docs/RELEASING.md)

问题和建议请提交 [Issue](https://github.com/zby909/stack-nest-server/issues)。[参与贡献](CONTRIBUTING.md)

## 许可

Copyright 2026 zby909 and StackNest contributors. 使用 [Apache-2.0](LICENSE) 许可，第三方内容保留原许可，见 [NOTICE](NOTICE)。
