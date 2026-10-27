# 文档

第一次使用，从 [Docker 部署](INSTALL_RELEASE.md) 开始。

仓库文档跟随 `main` 更新；安装已发布版本时，以部署包内的 README 和同名标签下的文档为准。

## 使用与部署

- [Docker 部署](INSTALL_RELEASE.md) — 下载版本、配置、启动。
- [家用电脑部署](HOME_SERVER_TUNNEL.md) — 无公网 IP 时使用 Cloudflare Tunnel。
- [静态站点与成员](USAGE.md) — 上传、预览、发布和权限。
- [Docker 应用](DOCKER_APPS.md) — 镜像、数据目录和资源限制。

## 运维与可选配置

- [运维](OPERATIONS.md) — 备份、升级、日志与排障。
- [换服务器](MIGRATION.md) — 导出、恢复和切换入口。
- [配置参考](DEPLOYMENT.md) — 环境变量、证书和入口。
- [磁盘硬配额](STORAGE_QUOTAS.md) — XFS 与存储要求。
- [应用联网](NETWORK_EGRESS.md) — 公网访问与局域网保护。
- [Tunnel 参考](CLOUDFLARE_TUNNEL.md) — 路由、缓存和连接器维护。

## 开发与原理

- [开发与测试](DEVELOPMENT.md)
- [源码 Docker 部署](GETTING_STARTED.md)
- [架构与工作原理](ARCHITECTURE.md)
- [产品范围](REQUIREMENTS.md)
- [版本发布](RELEASING.md)
- [Web 开发](https://github.com/zby909/stack-nest-web)

安装命令默认在 Linux Bash、`server/` 执行。实例私有路径可记在 [台账模板](INSTANCE.example.md)中。
