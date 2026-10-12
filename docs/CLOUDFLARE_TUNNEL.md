# Cloudflare Tunnel 路由与维护说明

家用电脑首次安装、Token 保存、systemd 配置及控制台操作见 [Docker + Cloudflare Tunnel 独立教程](HOME_SERVER_TUNNEL.md)。本文记录入口约束、缓存及迁移行为。普通公网服务器默认用 [HTTPS Docker 部署](GETTING_STARTED.md)，不依赖 Tunnel。

## 一个 Nginx，两种本机入口

`compose.tunnel-public.yaml` 覆盖 `nginx` 服务，替换端口为：

| 入口              | 容器监听 | 用途                 |
| ----------------- | -------- | -------------------- |
| `127.0.0.1:18081` | 81       | cloudflared 公网回源 |
| `127.0.0.1:18080` | 82       | SSH 本地访问         |

`cloudflare-tunnel.conf.template` 信任本机连接器的 HTTPS 与 CF-Connecting-IP；`ssh.conf.template` 不信任这些外来标头。18081 不能暴露给局域网或公网。基础模板仍在容器内 80 提供共同变量，其端口不发布宿主。**不再单独运行 `tunnel` Nginx 容器。**

源码部署选择 `COMPOSE_FILE=compose.yaml:compose.tunnel-public.yaml`；预构建模式使用 `compose.release.yaml` 替换基础文件。更新入口时在原项目目录执行：

```bash
docker compose up -d --no-deps nginx
docker compose exec -T nginx nginx -t
```

旧迁移包可能有独立 `tunnel` 服务，迁移工具仍可导入；本段命令只用于新版单 Nginx 布局。

## 允许的域名

| Host（以 example.com 为例）          | 行为                         |
| ------------------------------------ | ---------------------------- |
| `stack-nest.example.com`             | 配置的管理入口；API 需登录   |
| `demo.example.com`                   | 无依赖 HTTP 测试页，仅根路径 |
| `stack-nest-{slug}.example.com`      | Nginx 读取已发布静态目录     |
| `stack-nest--<UUID>.example.com`     | Go 按记录提供保留版本预览    |
| `stack-nest--app-{slug}.example.com` | Go 按记录转发 Docker 应用    |
| 其他 Host 或不存在项目               | 404                          |

允许主机的 HTTP 请求重定向 HTTPS。后台 `/sites/` 为 404，站点与管理入口使用不同来源。暂停项目的正式页面和资源返回 503，版本预览返回 404。缺失资源返回 404，普通页面路径允许 SPA 回退；隐藏文件禁止访问。WebSocket 与流式响应继续透传。

回源 HTTP 只发生在本机；浏览器到 Cloudflare、Cloudflare 到连接器均加密。HTTPS 会话使用 `__Host-stacknest_session`、Secure、HttpOnly、SameSite=Strict Cookie。邀请地址来自后端受信任配置，默认 `https://管理域名/login`。

## 缓存行为

正式静态项目统一 `Cache-Control: no-cache`，浏览器可存储，但每次使用前须验证。Nginx 默认 ETag 和 Last-Modified 命中时返回 304；没有额外文件 hash 或版本判断。API、版本预览、Docker 应用及错误响应继续禁止保存。

在 Cloudflare **缓存 → 配置 → 浏览器缓存 TTL** 选择 **尊重现有标头**。固定 TTL 可能覆盖 CSS 等资源的 `no-cache`，变成数小时强缓存；已有 Cache Rules 也要检查。更改后清理相关旧缓存，再核对经过 Cloudflare 的最终响应头、资源内容及 304。见 [Cloudflare 浏览器 TTL 说明](https://developers.cloudflare.com/cache/how-to/edge-browser-cache-ttl/set-browser-ttl/)。

## 连接器管理

`deploy/cloudflared.service` 使用宿主 systemd，Token 位于 root:root 0600 的 `/etc/cloudflared/tunnel-token`。LoadCredential 只向服务提供运行时文件；不要把 Token 放进命令参数、Compose、Git 或日志。

```bash
systemctl is-active cloudflared
systemctl is-enabled cloudflared
sudo journalctl -u cloudflared -n 50 --no-pager
sudo systemctl restart cloudflared
curl -fsS http://127.0.0.1:20241/ready
```

ready 200 只表示连接器已连接 Cloudflare，不证明 DNS 或站点正确。软件通过 [官方 apt 源](https://pkg.cloudflare.com/index.html)更新，指标端口只监听本机。保持 info 日志级别，避免记录敏感 HTTP 标头的 debug 日志。

## 迁移

实际地址及私有配置路径记录在实例台账；运行配置含凭据，不输出完整内容或提交仓库。

业务迁移包携带 Nginx 模板，不携带宿主 cloudflared Token、systemd 服务或 Cloudflare 账号配置。导入先启用 SSH 预览；验证数据后，激活原入口配置并在新主机配置连接器，再切换 Tunnel / DNS。不要让同一 Tunnel 的两个连接器使用不同版本或不一致数据。

官方参考：[Tunnel Token](https://developers.cloudflare.com/tunnel/reference/tunnel-tokens/)、[token-file 参数](https://developers.cloudflare.com/tunnel/reference/run-parameters/)。
