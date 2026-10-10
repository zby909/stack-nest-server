# Cloudflare Tunnel 公网入口

适用于已有 StackNest 实例、没有公网 IPv4 的主机。复用已有 Nginx 容器，在宿主机安装官方 cloudflared；不新增 Docker、数据库或反向代理服务。

```text
浏览器 HTTPS → Cloudflare → 加密 Tunnel → cloudflared
  → http://127.0.0.1:18081 → 现有 Nginx → 静态文件 / Go / 项目应用
SSH 本地访问 → http://127.0.0.1:18080 → 原有 Nginx HTTP 入口
```

回源 HTTP 只发生在本机；浏览器到 Cloudflare、Cloudflare 到 cloudflared 的连接均加密。域名 SSL/TLS 模式无需改成 Flexible。入口传递 HTTPS 信息，后台继续使用 `__Host-stacknest_session`、Secure、HttpOnly、SameSite=Strict Cookie。

## 入口与域名

| Host | 行为 |
| --- | --- |
| `stack-nest.zby909.top` | 管理端；API 仍需登录 |
| `demo.zby909.top` | 无依赖的 HTTP 测试页；其他路径 404 |
| `stack-nest-{项目标识}.zby909.top` | Nginx 读取正式版静态目录，遵守暂停标记；不存在的站点 404 |
| `stack-nest--*.zby909.top` | Go 按完整 Host 查询 Docker 应用或版本预览；不存在的记录 404 |
| 其他 Host | 404 |

正式静态站点使用 `Cache-Control: no-cache`，浏览器可保存文件，但每次使用前须验证。Nginx 沿用默认 ETag 和 Last-Modified；命中条件请求时返回 304，无需重新传输文件正文。错误页、版本预览与 Docker 应用仍禁止保存，管理 API 保留 `no-store`。不按后缀设置长期强缓存，也不新增内容 hash 或版本判断。

HTTP 请求在允许的域名上重定向到 HTTPS。后台 `/sites/` 返回 404，上传站点使用独立主机，不共享后台 Cookie。项目服务保留 WebSocket 和流式响应。

`18081` 必须只绑定宿主机 `127.0.0.1`：它信任 cloudflared 提供的 HTTPS 信息和 CF-Connecting-IP，不能直接暴露给公网或局域网。`18080` 是独立的 SSH HTTP 入口，不信任这些外来标头。

## Cloudflare 配置

在指定 Tunnel 的“已发布应用程序”添加一条路由：

| 字段 | 值 |
| --- | --- |
| 子域名 | `*` |
| 域 | `zby909.top` |
| 路径 | 留空 |
| 服务 URL | `http://127.0.0.1:18081` |
| HTTP 主机标头 | 保持默认，不覆盖 |

使用完整左侧通配符；`stack-nest-*` 这种局部通配符不能作为 Tunnel 主机规则。项目前缀由本机 Nginx 与 Go 校验。

DNS 中 `*` 和 `stack-nest` 应为启用代理的 CNAME，目标为该 Tunnel 的 `<UUID>.cfargotunnel.com`。若已有同名 A/AAAA/CNAME，应修改对应记录；明确的 `stack-nest` 记录优先于 `*`，不能继续指向旧服务器。`demo` 可使用泛域名记录，也可单独指向同一 Tunnel。保留 `codex-mcp`、`zby`、`zs` 等其他明确记录。

Token 只授权运行连接器，不能代替 Cloudflare API Token 修改 DNS。Tunnel 路由由 Cloudflare 控制台管理，本机不再维护一份重复的 ingress 配置。

在“缓存 → 配置”将 **浏览器缓存 TTL（Browser Cache TTL）** 设置为 **尊重现有标头（Respect Existing Headers）**，让浏览器收到源站的 `no-cache`。固定 TTL 可能覆盖源站响应，例如将 CSS 改为 `max-age=14400`（4 小时），导致更新后仍使用旧资源。已有 Cache Rules 也应尊重源站缓存头；参见 [Cloudflare 官方说明](https://developers.cloudflare.com/cache/how-to/edge-browser-cache-ttl/set-browser-ttl/)。验收以经过 Cloudflare 的最终响应头与 304 为准，不能只检查源站。

## Compose 回源配置

新增配置位于 `compose.tunnel-public.yaml` 和 `deploy/nginx/cloudflare-tunnel.conf.template`。前者给现有 tunnel 服务增加 `127.0.0.1:18081:81` 、模板挂载和站点数据卷的只读挂载；原有 `18080:80` 保留。

使用仓库版 Compose 时，在已有实例的项目名和环境文件下应用覆盖配置：

```bash
docker compose -p stacknest --env-file .env.compose \
  -f compose.release.yaml -f compose.tunnel-public.yaml \
  up -d --no-deps tunnel
```

当前 Hyper-V 实例使用迁移后已解析的 `/data/stacknest/runtime/compose.json`，已直接合并上述配置。该文件含业务密钥，禁止输出完整内容或提交仓库。`compose.public.json` 是原公网端口配置的备用副本；CGNAT 主机无需激活其 80/443。

## 邀请邮件地址

确认后台 HTTPS 可以访问后，将 API 的 `STACKNEST_INVITATION_LOGIN_URL` 配置为 `https://stack-nest.zby909.top/login`，然后重建 API 服务。当前 Hyper-V 实例已设置这个地址。

邀请地址来自受信任配置，不从请求 Host 推导。添加邮箱仍默认不勾选发送邀请；选中并确认后才会发送，不会自动创建登录会话或验证码。

## cloudflared 服务

通过 [Cloudflare 官方软件源](https://pkg.cloudflare.com/index.html)安装 cloudflared。当前服务使用 `deploy/cloudflared.service`，Token 经 SSH/SFTP 传入 `/etc/cloudflared/tunnel-token`；目录 root:root 0700、文件 root:root 0600。

systemd 的 LoadCredential 让动态非特权用户读取运行时凭据。ExecStart 只包含 Token 文件路径；Token 不写入命令参数、环境变量、Compose、项目包或日志。使用 info 日志级别，不打开可能记录 HTTP 敏感标头的 debug 日志。

安装或更新服务文件后：

```bash
sudo install -m 0644 deploy/cloudflared.service /etc/systemd/system/cloudflared.service
sudo systemd-analyze verify /etc/systemd/system/cloudflared.service
sudo systemctl daemon-reload
sudo systemctl enable --now cloudflared
```

查看、重启、停用：

```bash
systemctl is-active cloudflared
systemctl is-enabled cloudflared
sudo journalctl -u cloudflared -n 50 --no-pager
sudo systemctl restart cloudflared
sudo systemctl disable --now cloudflared
curl -fsS http://127.0.0.1:20241/ready
```

ready 返回 200 表示连接器已连接 Cloudflare，不代表 DNS 和应用已配置正确。指标入口只监听本机 `20241`。服务失败自动重启，软件升级通过官方 apt 源管理；不更改 SSH、Tailscale、XFS 或公网入站端口。

## 验证与迁移

```bash
curl -fsS http://127.0.0.1:18081/ \
  -H 'Host: demo.zby909.top' -H 'X-Forwarded-Proto: https'
curl -fsS https://demo.zby909.top/
curl -I https://stack-nest.zby909.top/
```

验收静态项目时要核对实际 HTML 与 CSS 内容及对应 Content-Type，不能只看 200；API 默认提示也可能返回 200。另检查 SPA 深层路径、正式站点暂停后页面及资源返回 503、隐藏文件和不存在的项目返回 404。版本预览在项目暂停时返回 404；恢复后应重新可用。

测试页包含 `stacknest-tunnel-demo-v1-20261010` 标记；同时验证未知域名 404、未登录管理 API 401、HTTP 跳转 HTTPS。浏览器登录后再检查项目打开、上传和会话。

业务迁移包会携带新的 Nginx 模板，但不会携带原生 cloudflared 的 Token、systemd 服务或 Cloudflare 账号配置。导入仍只启用 SSH 预览端口；新主机验证业务后，重新配置回源 `18081` 和 cloudflared，再切换 Tunnel / DNS。不要让旧、新两台连接器在共用同一 Tunnel 时提供不同版本或不完整的数据。

官方参考：[Tunnel Token](https://developers.cloudflare.com/tunnel/reference/tunnel-tokens/)、[运行参数与 token-file](https://developers.cloudflare.com/tunnel/reference/run-parameters/)。
