# 家用电脑部署：Docker Compose + Cloudflare Tunnel

适合没有公网 IP、运营商封锁入站端口或处于 CGNAT 的家庭电脑。StackNest 四个核心服务仍用 Docker；宿主 Ubuntu 上增加一个 `cloudflared` systemd 服务，负责主动连接 Cloudflare。

```text
浏览器 HTTPS
    ↓
Cloudflare（你的域名与边缘证书）
    ↓ 加密隧道，由家用电脑主动建立
Ubuntu cloudflared
    ↓ http://127.0.0.1:18081
同一个 Nginx 容器
    ├─ 管理页面与 Go API
    ├─ 已发布静态项目
    └─ Docker 应用 / 版本预览 → Go → 对应服务
```

不需要公网 IPv4、路由器端口转发或开放公网 SSH。家用电脑必须持续开机并能访问 Cloudflare；断电、休眠或网络中断都会影响访问。Tunnel 是可选入口，迁到普通公网服务器后可改回 HTTPS 部署。

## 1. 准备 Ubuntu 和 Docker

使用 Ubuntu 实体机，或 Windows Hyper-V / 其他虚拟机中的 Ubuntu Server。以下步骤全部在 **Ubuntu Bash** 里执行，不在 Windows PowerShell 中执行。虚拟机需要正常出站联网和足够的磁盘、内存；不要求桥接公网。

优先按 [版本镜像部署第 1、2 节](INSTALL_RELEASE.md) 下载部署包、准备 Docker Engine、Compose 2.24.4+、SMTP 和随机密钥。无需另取前端源码；自行构建时才按 [源码部署](GETTING_STARTED.md) 准备并排的 `server/`、`web/`。

自己的域名须已托管到 Cloudflare。下面统一用 `example.com`，替换成自己的域名：

```dotenv
COMPOSE_PROJECT_NAME=stacknest
COMPOSE_FILE=compose.release.yaml:compose.tunnel-public.yaml
STACKNEST_ADMIN_DOMAIN=stack-nest.example.com
STACKNEST_BASE_DOMAIN=example.com
```

源码部署将上述基础文件改为 `compose.yaml`，启动时使用 `docker compose up -d --build`；下文默认使用发布包。

这个入口**不使用** `compose.tls.yaml`，也不需要在 Ubuntu 放置 HTTPS 证书。浏览器 HTTPS 由 Cloudflare 提供；cloudflared 与 Nginx 之间的 HTTP 只在本机回环地址上流动。不要为了 Tunnel 把整个域名的 SSL/TLS 模式改成 Flexible。

## 2. 先启动 StackNest

在 `server/` 执行：

```bash
docker compose config --quiet
docker compose pull
docker compose up -d --wait --wait-timeout 120
docker compose ps -a
docker compose exec -T nginx nginx -t
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
curl -fsS http://127.0.0.1:18081/ \
  -H 'Host: demo.example.com' -H 'X-Forwarded-Proto: https'
```

最后一个请求应显示“Cloudflare Tunnel 已连通”。入口现在监听：

| 宿主地址                     | 用途                                  |
| ---------------------------- | ------------------------------------- |
| `127.0.0.1:18081` → Nginx 81 | cloudflared 回源，保留 HTTPS 信息     |
| `127.0.0.1:18080` → Nginx 82 | 可选 SSH 本地访问，浏览器用 localhost |

这些端口只能监听 `127.0.0.1`，**不能改成 `0.0.0.0`**。18081 信任来自本机连接器的转发标头；直接公开会让外部请求伪造这些标头。数据库、Go API 和 Runner 均不开放宿主端口。

## 3. 创建 Tunnel

在 Cloudflare 控制台进入 **Zero Trust → Networks / 网络 → Tunnels / 隧道**（菜单名称可能调整），创建一个 Cloudflared 类型的 Tunnel，例如 `stacknest-home`。参考 [Cloudflare 官方创建教程](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel/)。

在连接器安装页取出这个 Tunnel 的 Token。Token 是连接器凭据，不是用于 DNS 管理的 API Token。下面使用自己的 systemd 模板，因此无需执行面板中包含 `--token` 的完整安装命令。

## 4. 从官方源安装 cloudflared

按 [Cloudflare 官方 apt 源](https://pkg.cloudflare.com/index.html)安装：

```bash
sudo apt-get install -y curl ca-certificates
sudo mkdir -p --mode=0755 /usr/share/keyrings
curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg \
  | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null
echo 'deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main' \
  | sudo tee /etc/apt/sources.list.d/cloudflared.list
sudo apt-get update
sudo apt-get install -y cloudflared
cloudflared --version
```

首次保存 Token，在 Ubuntu 终端直接执行下面的交互命令，提示后粘贴 **Token 本身**。输入不会回显，也不会成为命令历史中的字面量：

```bash
sudo -v
sudo install -d -m 0700 /etc/cloudflared
read -r -s -p '粘贴 Tunnel Token: ' stacknest_tunnel_token
printf '\n'
if [ -n "$stacknest_tunnel_token" ]; then
  printf '%s' "$stacknest_tunnel_token" \
    | sudo sh -c 'umask 077; cat > /etc/cloudflared/tunnel-token'
fi
unset stacknest_tunnel_token
sudo test -s /etc/cloudflared/tunnel-token
sudo chmod 600 /etc/cloudflared/tunnel-token
```

不要把 Token 放进 `.env`、Compose、仓库、聊天或命令参数；不要读取它来展示日志。已有连接器服务时先确认是否承载其他网站，不能直接覆盖它。

## 5. 配置开机启动

首次安装本实例连接器时，在 `server/` 执行：

```bash
sudo install -m 0644 deploy/cloudflared.service /etc/systemd/system/cloudflared.service
sudo systemd-analyze verify /etc/systemd/system/cloudflared.service
sudo systemctl daemon-reload
sudo systemctl enable --now cloudflared
systemctl is-active cloudflared
systemctl is-enabled cloudflared
curl -fsS http://127.0.0.1:20241/ready
```

状态应为 `active`、`enabled`，ready 返回 200。服务以动态非特权用户运行，用 systemd `LoadCredential` 读取运行时 Token；命令只包含文件路径，失败后自动重启。指标端口只监听本机。

如果连接失败，检查：

```bash
sudo journalctl -u cloudflared -n 50 --no-pager
```

日志应有已建立 Tunnel 连接的记录。局域网出站策略须允许 cloudflared 连接 Cloudflare；具体要求见 [官方网络说明](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/configure-tunnels/tunnel-with-firewall/)。不需要对家庭路由器添加入站规则。

## 6. 添加已发布应用程序和 DNS

在刚创建的 Tunnel 中添加“已发布应用程序 / Public hostname”：

| 字段           | 值                       |
| -------------- | ------------------------ |
| 子域名         | `*`                      |
| 域             | `example.com`            |
| 路径           | 留空                     |
| 服务 URL       | `http://127.0.0.1:18081` |
| HTTP 主机标头  | 保持默认，不覆盖         |
| TLS / 其他设置 | 保持默认                 |

这是一条完整左侧泛域名规则。不要写 `stack-nest-*`；项目前缀由 Nginx 和 Go 校验。Nginx 只正常路由管理域名、`demo.example.com` 和符合命名规则的已有项目，其他子域名返回 404。测试页不展示服务器信息。

控制台通常会创建指向 `<Tunnel-UUID>.cfargotunnel.com` 的代理 CNAME；在 DNS 页面检查：

- `*.example.com` 指向该 Tunnel，并启用橙云。
- 若有旧的 `stack-nest.example.com` A/AAAA/CNAME，改为指向这个 Tunnel 的代理 CNAME。精确记录优先于泛域名，不能保留旧 IP。
- `demo.example.com` 可使用泛域名，也可单独指向同一个 Tunnel。
- 保留其他网站的明确记录及 Tunnel；不要为了本项目删除整个域名的其他解析。

此前配置过 Origin Rules 时，确认没有把本项目错误转发到别的服务或端口。只修改匹配本项目的配置，避免影响其他网站。

在 **缓存 → 配置 → 浏览器缓存 TTL** 选择 **尊重现有标头**；不要为本项目设置覆盖源站缓存头或“缓存所有内容”。已发布静态资源使用 `no-cache` 协商缓存，管理 API 与动态响应使用 `no-store`。旧 CSS 仍未更新时清理相关旧缓存，详见 [缓存和路由说明](CLOUDFLARE_TUNNEL.md)。

## 7. 验收

```bash
curl -fsS https://demo.example.com/
curl -fsS https://stack-nest.example.com/api/health
curl -i https://stack-nest.example.com/api/projects
curl -i https://unexpected.example.com/
```

依次应为测试页面、健康 JSON、未登录 401、未知主机 404。HTTP 访问允许的主机应重定向 HTTPS。再按 [首次部署第 5 节](GETTING_STARTED.md#5-完成一次实际部署)检查邮箱登录、上传预览、正式站点和 Docker 应用。

在浏览器访问 `https://stack-nest.example.com/`，不要使用 `http://家庭IP:18081`。邀请邮件默认使用配置的管理域名 HTTPS 登录地址，不会使用 SSH 或家庭内网地址。

## 8. 日常维护和迁移

```bash
# 连接器
sudo systemctl restart cloudflared
sudo journalctl -u cloudflared -n 50 --no-pager
sudo apt-get update
sudo apt-get install --only-upgrade cloudflared
sudo systemctl restart cloudflared

# Docker 平台；仍在 server/ 下执行
docker compose ps -a
docker compose logs --tail=50 stacknest runner nginx
```

修改 Token 后重启 cloudflared；泄露时先在 Cloudflare 轮换，再安全替换本机文件。停用此入口可执行 `sudo systemctl disable --now cloudflared`，不会删除平台数据。

云主机、实体 Ubuntu 和家用虚拟机使用同一套 Docker 数据格式；[完整迁移工具](MIGRATION.md)负责镜像和业务数据。连接器 Token、systemd 服务和 Cloudflare 账号配置不进入业务迁移包，需在新机器重新配置。确认新实例数据一致后再切换连接器，避免同一 Tunnel 的多个连接器指向不同数据。

换成公网服务器时，在新实例选择 `compose.tls.yaml`，配置可信证书和 DNS，再停用旧 Tunnel。业务内容不用重新上传。
