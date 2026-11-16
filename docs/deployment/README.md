# 部署与运维

除特别说明外，命令在 Linux Bash 中的 `server/` 根目录执行。

## 安装平台

- [Docker 部署](INSTALL_RELEASE.md) — 在 Linux 服务器下载、配置并启动平台。
- [家用电脑部署](HOME_SERVER_TUNNEL.md) — 通过 Cloudflare Tunnel 使用没有公网 IP 的电脑或虚拟机。
- [从源码部署](GETTING_STARTED.md) — 从配套的后端与前端源码构建镜像。

首次安装后，参考 [示例站点](../../examples/README.md)完成预览与发布。

## 维护实例

- [日常运维](OPERATIONS.md) — 检查状态、备份、升级与排障。
- [恢复演练](RESTORE.md) — 在隔离实例验证日常备份。
- [换服务器](MIGRATION.md) — 导出完整迁移包、在目标机验收并切换入口。

## 配置参考

- [环境变量、入口与证书](DEPLOYMENT.md)
- [Cloudflare 路由、缓存与连接器](CLOUDFLARE_TUNNEL.md)
- [Docker 磁盘硬配额](STORAGE_QUOTAS.md)
- [应用联网与局域网保护](NETWORK_EGRESS.md)
- [私有实例记录模板](INSTANCE.example.md)
