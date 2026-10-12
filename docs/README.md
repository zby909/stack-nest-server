# 文档导航

StackNest 是个人开发者和中小型公司的自托管项目管理平台，支持静态站点及单容器 Docker HTTP 应用。按自己的场景选择入口：

| 你要做什么                    | 入口                                                  |
| ----------------------------- | ----------------------------------------------------- |
| 下载镜像部署普通 Linux 服务器 | [版本镜像部署](INSTALL_RELEASE.md)                    |
| 从源码构建平台                | [源码 Docker 部署](GETTING_STARTED.md)                |
| 维护者发布版本镜像            | [GitHub Actions 发布](RELEASING.md)                   |
| 家用电脑没有公网 IP           | [家用电脑 + Cloudflare Tunnel](HOME_SERVER_TUNNEL.md) |
| 配置环境、HTTPS 和入口覆盖    | [部署配置](DEPLOYMENT.md)                             |
| 查看架构、模块和权限          | [架构与维护边界](ARCHITECTURE.md)                     |
| 备份、升级、恢复或排障        | [日常运维](OPERATIONS.md)                             |
| 整体换服务器                  | [完整迁移](MIGRATION.md)                              |
| 部署自己的 Docker 应用        | [Docker 应用](DOCKER_APPS.md)                         |
| 配置应用磁盘硬配额            | [XFS 与硬配额](STORAGE_QUOTAS.md)                     |
| 允许应用联网并保护局域网      | [应用网络](NETWORK_EGRESS.md)                         |
| 维护 Tunnel 路由和缓存        | [Tunnel 参考](CLOUDFLARE_TUNNEL.md)                   |
| 查功能与上线验收标准          | [需求与验收](REQUIREMENTS.md)                         |
| 查看最新检查结果              | [检查记录](V1_REVIEW.md)                              |

开发从 [Server README](../README.md) 进入；前端另有 [Web README](https://github.com/zby909/stack-nest-web/blob/main/README.md) 和 [浏览器回归](https://github.com/zby909/stack-nest-web/blob/main/docs/TESTING.md)。

## 阅读约定

- 命令默认在 Linux Bash、`server/` 执行；Windows 开发脚本会明确标注 PowerShell。
- 示例域名、目录和 IP 必须替换；项目名及 Compose 参数与实际启动方式一致。
- 硬配额、联网保护、备份定时器、家用 Tunnel 都是可选功能，各有前置条件。
- 上传内容默认公开；用户隔离保护后台管理权限，版本预览持有链接即可访问。
- 新环境仍需真实登录、站点和应用验收，构建成功或容器运行不是完整验收。
- [历史档案](history/V1_REVIEW.md)中的地址、版本和测试数量不是当前部署默认值。

私有路径和本机配置记录在实例台账。复制 [台账模板](INSTANCE.example.md)为 `docs/INSTANCE.md`，后者被 Git 忽略，不保存授权码、Token 或私钥内容。
