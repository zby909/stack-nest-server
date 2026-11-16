# 家用电脑部署

适合没有公网 IP 的 Ubuntu 电脑或虚拟机。平台仍使用 Docker，宿主运行 cloudflared 将请求带到本机入口。[连接原理](ARCHITECTURE.md) · [路由与缓存参考](CLOUDFLARE_TUNNEL.md)

准备托管在 Cloudflare 的域名和可正常出站联网的 Ubuntu。命令均在 Ubuntu Bash 执行。

## 1. 启动平台

按 [安装教程第 1、2 步](INSTALL_RELEASE.md#1-下载)下载部署包、填写邮箱与密钥，然后修改 `.env`：

```dotenv
COMPOSE_FILE=compose.release.yaml:compose.tunnel-public.yaml
STACKNEST_ADMIN_DOMAIN=stack-nest.example.com
STACKNEST_BASE_DOMAIN=example.com
```

源码部署使用 `compose.yaml` 作为基础文件。这个入口无需本机 HTTPS 证书。

```bash
docker compose config --quiet
docker compose pull
docker compose up -d --wait --wait-timeout 120
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
curl -fsS http://127.0.0.1:18081/ -H 'Host: demo.example.com' -H 'X-Forwarded-Proto: https'
```

测试页应显示 Tunnel 连通提示。保留入口的回环绑定，不把 18081 改为公开端口。

## 2. 创建 Tunnel

在 Cloudflare 控制台的 **Networking → Tunnels** 创建隧道，取得 [连接器 Token](https://developers.cloudflare.com/tunnel/reference/tunnel-tokens/)。

安装 [官方 apt 源](https://pkg.cloudflare.com/index.html)的软件：

```bash
sudo apt-get install -y curl ca-certificates
sudo mkdir -p --mode=0755 /usr/share/keyrings
curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null
echo 'deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main' | sudo tee /etc/apt/sources.list.d/cloudflared.list
sudo apt-get update
sudo apt-get install -y cloudflared
```

交互保存 Token，输入不会回显：

```bash
sudo -v
sudo install -d -m 0700 /etc/cloudflared
read -r -s -p '粘贴 Tunnel Token: ' stacknest_tunnel_token
printf '\n'
if [ -n "$stacknest_tunnel_token" ]; then
  printf '%s' "$stacknest_tunnel_token" | sudo sh -c 'umask 077; cat > /etc/cloudflared/tunnel-token'
fi
unset stacknest_tunnel_token
sudo test -s /etc/cloudflared/tunnel-token
sudo chmod 600 /etc/cloudflared/tunnel-token
```

Token 不放入仓库或命令参数。已有连接器承载其他网站时，先确认用途再调整服务。

## 3. 配置服务

在 `server/` 执行：

```bash
sudo install -m 0644 deploy/cloudflared.service /etc/systemd/system/cloudflared.service
sudo systemd-analyze verify /etc/systemd/system/cloudflared.service
sudo systemctl daemon-reload
sudo systemctl enable --now cloudflared
curl -fsS http://127.0.0.1:20241/ready
```

ready 应返回 200。失败时查看 `sudo journalctl -u cloudflared -n 50 --no-pager`。

## 4. 配置域名

打开该隧道，在 **Routes → Add route** 选“已发布应用程序”：

| 字段            | 值                               |
| --------------- | -------------------------------- |
| 子域名          | `*`                              |
| 域              | 自己的基础域名，如 `example.com` |
| 服务 URL        | `http://127.0.0.1:18081`         |
| 路径、Host 覆盖 | 留空或默认                       |

在 DNS 检查 `*.example.com` 有代理开启的 CNAME，指向 `<隧道 UUID>.cfargotunnel.com`。若管理域名有单独的记录，也将它指向同一隧道。[Cloudflare 路由说明](https://developers.cloudflare.com/tunnel/concepts/routing/#dns-records)

核对旧 Origin Rules 的回源端口，浏览器缓存 TTL 见 [缓存配置](CLOUDFLARE_TUNNEL.md#缓存)。

## 5. 验证

```bash
curl -fsS https://demo.example.com/
curl -fsS https://stack-nest.example.com/api/health
curl -i https://stack-nest.example.com/api/projects
curl -i https://unexpected.example.com/
```

应依次得到测试页、健康 JSON、401、404。最后在管理域名完成邮箱登录与一次静态发布。

## Hyper-V 网段冲突

Windows 重启后，Default Switch 网段可能变化。若 Tunnel 返回 502，用 `ip -4 route` 和 `docker network inspect stacknest_default` 检查虚拟机与 Docker 网段是否重叠。网络名随 Compose 项目名变化。

可在自建的 `compose.network.yaml` 固定平台网段：

```yaml
networks:
  default:
    ipam:
      config:
        - subnet: 10.203.0.0/24
```

将示例网段换成与局域网、VPN 和其他 Docker 网络均不重叠的网段，在 `COMPOSE_FILE` 末尾追加 `:compose.network.yaml`。已有实例更换网段前先备份，重建时保留命名卷。应用各自的网络单独检查。

[连接器维护](CLOUDFLARE_TUNNEL.md) · [备份](OPERATIONS.md) · [换服务器](MIGRATION.md)
