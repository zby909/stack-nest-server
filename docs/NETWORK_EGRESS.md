# Docker 应用联网与局域网保护

适用于个人开发者和中小型公司的自建实例。每个应用仍有独立 Docker bridge；启用本功能后，可以访问公网和 Linux 宿主自身可达的服务，禁止直接访问宿主所在局域网的其他设备。未启用时保持原有 `internal` 网络，不主动访问公网。

## 访问边界

| 目标                                        | 启用后的行为                                  |
| ------------------------------------------- | --------------------------------------------- |
| 公网网站、API、镜像仓库                     | 允许，应用仍受目标服务自身权限限制            |
| Linux 宿主自身地址上的服务                  | 允许；只监听 127.0.0.1 的服务不会因此自动暴露 |
| 宿主发布的 Docker 端口                      | 允许，使用宿主地址和发布端口                  |
| Linux 网卡所在局域网的其他 IPv4 / IPv6 地址 | 阻止直接访问                                  |
| 管理员另行列出的局域网网段                  | 阻止直接访问                                  |
| 管理员指定的单个 IP、TCP/UDP 端口例外       | 允许该端口                                    |
| 其他应用的私有 bridge 地址                  | 继续由 Docker 的网桥隔离限制                  |

这是一条直接网络访问边界。管理员主动开放的宿主代理、VPN 出口或业务转发服务仍按该服务的权限工作；本功能不会检查经过允许服务的第二跳。Tailscale / 其他 VPN 网段不会自动全部加入禁止列表，需要时明确列为额外网段。

**静态站点的 JavaScript 由访客浏览器发请求，不受这套容器规则影响。** Docker 应用的后端、SSR 和由后端代发的请求受限制。

## 工作方式

宿主 Go 服务只管理独立的 `inet stacknest_egress` nftables 表，不切换 Docker 的防火墙后端，也不修改 Docker 自己的表。支持当前使用 iptables 的 Docker。

Runner 为应用创建以 `sne-` 开头的 bridge，并将应用网关的优先级设低，保留 Runner 的平台网络出口。用户应用和 Runner 均不增加 NET_ADMIN。

规则在 DNAT 之前识别宿主本机地址，因此宿主发布的容器端口也能正常访问。应用回复已建立的访客连接不受出站禁止影响。其余到指定局域网的请求丢弃；现有宿主防火墙规则仍可进一步限制访问。

宿主服务每 5 秒核对规则、接口地址和配置。配置错误时保留上次规则，并将状态置为不可用；Runner 拒绝新的部署、启动和重启，停止、删除及已有应用代理仍可用。缺少状态或状态超过 30 秒也会拒绝新的联网操作。

Docker 开机前等待服务加载规则，并执行就绪检查。已有规则不会因规则服务退出而被主动删除；管理员清空宿主防火墙后会有最长约 5 秒的恢复窗口，不能将此机制当作对抗宿主管理员的安全隔离。

## 新实例启用

运行时需要 Linux、iproute2、nftables，以及 Docker Engine API 1.48+（Docker 28+）。发布包已附带 Linux amd64 二进制，在 `server/` 执行：

```bash
sudo apt-get install -y iproute2 nftables
ip -br address
sudo bin/stacknest-egress install --interface eth0
sudo /usr/local/bin/stacknest-egress check
```

自行从源码构建时才需要项目 `go.mod` 声明的 Go 版本，使用以下方式：

```bash
sudo apt-get install -y iproute2 nftables
mkdir -p .local-dev/build
go build -trimpath -o .local-dev/build/stacknest-egress ./cmd/stacknest-egress
ip -br address
ip route
sudo .local-dev/build/stacknest-egress install --interface eth0
sudo /usr/local/bin/stacknest-egress check
```

替换 `eth0` 为实际连接局域网的网卡；多网卡可重复指定 `--interface`。不指定时仅自动识别有地址的物理/虚拟以太网接口，排除 Docker bridge 和 VPN。不要额外启用一个会清空规则的通用 nftables.service。

使用 Hyper-V NAT 的实例必须另外配置 Windows 宿主接入的真实家庭局域网。Ubuntu 通常只能看到 NAT 网段，无法自动推断 NAT 外的家庭 LAN。例如真实家庭网络为 `192.168.2.0/24` 时：

```bash
sudo .local-dev/build/stacknest-egress install --interface eth0 --block-cidr 192.168.2.0/24
```

上例用于首次安装。已有配置时，安装器拒绝用参数覆盖；由超管在“网络设置”页面修改，或用 `sudoedit /etc/stacknest-egress/config.json` 修改。不要照抄示例网段。存在 IPv6 家庭网络时也加入其实际前缀。

之后将可选配置叠加到原有 Compose，保留原先的认证、入口及配额配置：

```bash
docker compose -p stacknest -f compose.yaml -f compose.tls.yaml -f compose.egress.yaml up -d
```

发布包使用 `compose.release.yaml` 作为基础文件。若原来使用其他文件或项目名，沿用原来的启动命令，只增加 `-f compose.egress.yaml`。硬磁盘配额仍需原有 `compose.quota.yaml`。Compose 状态目录挂载为只读且禁止自动创建空目录；缺少宿主规则服务时 Runner 会拒绝启动，不会静默放开网络。

## 已有应用切换

Docker network 的 `internal` 属性不能原地修改。旧应用网络与启用后的网络不一致时，Runner 会明确拒绝；不要只添加环境变量后直接期待旧容器继续运行。

维护窗口内先做现有备份，停止后台 API，使用旧 Runner 的 `apps-freeze` 停止应用，并保存带本实例标签的容器配置作为恢复依据。仅移除经过标签核对的本实例应用容器和其私有网络，**保留 `apps/{UUID}/data`、镜像和 PostgreSQL**；断开 Runner 在这些私有网络上的端点后再删除网络。

安装宿主规则服务、叠加联网配置，再启动 Runner 并执行 `apps-thaw`，最后启动 API。后台会按数据库中的固定镜像和配置重建原先要求运行的丢失容器；用户原先停止的应用保持停止，下次启动时重建。验证原有数据、域名、停启状态与资源配额。失败时恢复原配置和私有网络，不删除业务数据。

## 页面中的简化配置

超级管理员进入“网络设置”后，先选择服务器连接家庭或公司网络的网卡；服务已读取其 IPv4 / IPv6 地址及前缀，自动纳入对应网段，Linux 本机地址仍允许访问。

Hyper-V 等 NAT 虚拟机只能直接识别自己的虚拟网络，不能可靠推断 Windows 宿主外部的家庭局域网。展开“补充家庭网络 / 虚拟机外的局域网”后，可填写 Windows 的 IPv4 地址和子网掩码（也接受前缀长度），或粘贴中英文 ipconfig 输出。页面按每张网卡的地址和掩码计算私有 IPv4 网段；多网卡需要明确勾选，不会默认全部加入。DNS 和网关不用于推断范围，缺少掩码时不猜测 /24。

识别和计算仅在当前浏览器进行，粘贴原文不会提交或保存。添加结果与原有额外网段合并并去重，点击网段关闭按钮可从待保存配置移除；这些操作尚不生效。最后点击“保存网络配置”，在弹窗核对服务器网卡、额外网段、DNS 和例外后确认，才写入宿主规则。

手动 CIDR、容器 DNS 和端口例外保留在高级设置。简化导入只处理私有 IPv4；Windows ipconfig 通常不包含 IPv6 前缀长度，虚拟机外的 IPv6 网络须通过高级设置填写实际前缀，不能根据地址猜测 /64。服务器选定网卡的 IPv6 仍由原有自动识别保护。

## 配置及 DNS

`/etc/stacknest-egress/config.json` 保存宿主配置，不接受普通用户或普通管理员修改。平台的“网络设置”页由超级管理员读取和更新；更新先核验并加载规则，再原子保存，支持配置版本冲突检查。示例：

```json
{
  "lanInterfaces": ["eth0"],
  "blockedCIDRs": ["192.168.2.0/24"],
  "allow": [{ "address": "192.168.2.10", "port": 5432, "protocol": "tcp" }],
  "dnsServers": ["1.1.1.1", "8.8.8.8"]
}
```

`allow` 默认空。网卡实际 IPv4 / IPv6 网段与额外 `blockedCIDRs` 合并。本机地址先放行，允许管理员指定 IP 加端口的最小例外；例外不会开放该设备的其他端口。

应用通过 Docker 内置 DNS 使用配置的公网 DNS，避免向被禁止的局域网路由器请求解析。可改为本地区域可达的公共 DNS，例如 `223.5.5.5`；上限 3 个。更改 DNS 仅对之后创建的应用容器生效，已有容器需重新部署；规则及 IP/端口例外自动生效。域名解析不会改变目标地址的网络限制。

## 核验和维护

```bash
systemctl status stacknest-egress.service --no-pager
journalctl -u stacknest-egress.service --since "10 minutes ago"
sudo /usr/local/bin/stacknest-egress check
sudo nft list table inet stacknest_egress
cat /run/stacknest-egress/status.json
systemctl show docker -p After -p ExecStartPre
```

配置修改后等一个刷新周期，再核验状态；不需要重启 Docker。安装器不立即重启 Docker，也不修改 SSH、Tailscale、Cloudflare Tunnel 或数据盘。

回归测试：

```bash
go test ./internal/egress ./internal/dockerrunner ./internal/auth ./internal/httpserver
sudo python3 deploy/check_egress_network.py
```

最后一条需要 Python 3，仅作为测试夹具创建独立网络及挂载命名空间，调用生产 Go 二进制建立虚拟应用、局域网设备和公网目标，实际验证 IPv4、IPv6、宿主发布端口、返回流量、单端口例外和规则恢复。不会连接或扫描真实局域网设备，不改变宿主规则。

## 迁移

迁移包携带本服务的 Go 二进制（内嵌 systemd unit），但不会搬走旧机器的局域网配置或整个宿主防火墙。新机器须按自己的网卡和 LAN 安装规则服务后再导入；迁移工具会在写入目标目录前检查就绪状态。

停用时先在维护窗口切回 internal 网络并重建应用，再移除 `/etc/systemd/system/docker.service.d/stacknest-egress.conf`、执行 `systemctl daemon-reload`，最后停用规则服务并删除自己的 nft 表。不要对仍在联网的应用直接撤销局域网保护。

## 超级管理员与控制接口

`STACKNEST_AUTH_SUPER_ADMIN_EMAIL` 指定一位已有管理员，留空时使用 `STACKNEST_AUTH_ADMIN_EMAIL`。普通邮箱管理接口不能授予超管身份；数据库继续保存基础管理员角色，每次会话查询由部署配置计算超管身份，既有会话刷新即可生效。指定普通用户会使启动失败，避免误提升权限。

超管继承现有管理员权限，额外可在 `/network` 配置网卡、禁止网段、IP/端口例外和公共 DNS，保存前有确认，更新记入操作日志。其他管理员和普通用户不能读取或更新网络接口。网络服务未安装时，页面显示不可用及排查提示。

宿主服务源码位于 `cmd/stacknest-egress` 和 `internal/egress`。它使用 `CAP_NET_ADMIN` 管理自己的 nft 表，通过 `/run/stacknest-egress/control.sock` 仅接收固定结构的读取/更新操作，不接受命令、路径或任意 nft 规则。Socket 模式为 0600，所有者是后台镜像固定 UID 10001；Compose 只给后台只读挂载此目录，不赋予后台 NET_ADMIN 或 Docker socket，用户应用也不挂载控制目录。虽然目录只读，仍可连接 Unix socket；宿主服务负责落盘。

从旧 Python 版本升级时，`stacknest-egress install` 自动保留并迁移 `/etc/stacknest-egress.json`，旧配置备份存放于 `/etc/stacknest-egress/legacy-config.json`；之后以新配置路径为准。安装器不删除旧脚本，部署确认后可移除不再使用的旧服务文件。Python 不再是此网络功能的运行时依赖；备份、迁移和隔离测试工具仍使用 Python。
