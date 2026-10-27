# Cloudflare 参考

首次安装见 [家用电脑教程](HOME_SERVER_TUNNEL.md)，连接和分流原理见 [架构](ARCHITECTURE.md)。

## 入口与路由

同一个 Nginx 提供两个回环入口：

| 宿主地址          | 用途                                 |
| ----------------- | ------------------------------------ |
| `127.0.0.1:18081` | cloudflared 回源，信任本机连接器标头 |
| `127.0.0.1:18080` | SSH 本地访问                         |

18081 保持回环绑定。Tunnel 的 Host 不覆盖，由 Nginx 按实际域名分流：

| Host                     | 行为           |
| ------------------------ | -------------- |
| 配置的管理域名           | 前端及管理 API |
| `demo.基础域名`          | 无依赖测试页   |
| `stack-nest-{slug}`      | 正式静态文件   |
| `stack-nest--{版本UUID}` | 保留版本预览   |
| `stack-nest--app-{slug}` | Docker 应用    |
| 其他 / 不存在的项目      | 404            |

## 缓存

正式静态文件使用 `no-cache` 和 Nginx 默认 ETag、Last-Modified。其余动态响应和错误使用 `no-store`。

Cloudflare 浏览器缓存 TTL 选择“尊重现有标头”。固定 TTL 或 Cache Rules 可能使同名 CSS 保留旧内容；调整后清理相关缓存，再检查最终响应头与内容。

## 连接器维护

```bash
systemctl is-active cloudflared
systemctl is-enabled cloudflared
curl -fsS http://127.0.0.1:20241/ready
sudo journalctl -u cloudflared -n 50 --no-pager
sudo systemctl restart cloudflared
```

ready 200 表示连接器在线，域名与站点另行验证。

按本教程通过官方 apt 源安装的连接器，用 apt 更新：

```bash
sudo apt-get update
sudo apt-get install --only-upgrade cloudflared
sudo systemctl restart cloudflared
cloudflared --version
curl -fsS http://127.0.0.1:20241/ready
```

`update` 刷新软件列表，`install --only-upgrade` 更新指定软件包；重启服务后，新程序才会用于隧道连接。更新会短暂影响现有连接，完成后再访问管理域名确认恢复。[官方更新说明](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/update-cloudflared/)

Token 位于 `/etc/cloudflared/tunnel-token`，root 所有、权限 0600，通过 systemd LoadCredential 使用。轮换后替换文件并重启服务；不要打印 Token 或使用 debug 记录请求头。

## 换服务器

业务迁移包带 Nginx 模板，不带 Tunnel Token 或账号配置。在目标机安装连接器，确认数据一致后切换，避免同一 Tunnel 的连接器指向不同数据。

[迁移步骤](MIGRATION.md) · [Cloudflare Token 文档](https://developers.cloudflare.com/tunnel/reference/tunnel-tokens/)
