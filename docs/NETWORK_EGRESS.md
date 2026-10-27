# 应用联网与局域网保护

可选功能。开启后 Docker 应用可访问公网及 Linux 本机服务，其他配置的局域网目标被拦截。静态网站的 JavaScript 请求来自访问者浏览器，不受这些容器规则约束。

默认应用网络为 internal。防火墙与 NAT 的工作方式见 [原理](ARCHITECTURE.md#联网保护)。

## 1. 安装宿主服务

需要 Linux、iproute2、nftables、Docker Engine 28+。发布包附带 `bin/stacknest-egress`：

```bash
sudo apt-get install -y iproute2 nftables
ip -br address
sudo bin/stacknest-egress install --interface eth0
sudo /usr/local/bin/stacknest-egress check
```

将 `eth0` 换成连接局域网的实际网卡，多网卡可重复指定。已有配置不会被安装参数覆盖。

源码构建用 `go build -trimpath -o .local-dev/build/stacknest-egress ./cmd/stacknest-egress`，再使用生成的程序 install。

## 2. 启用 Compose 配置

在原 `COMPOSE_FILE` 后追加 `:compose.egress.yaml`，保留已有入口和配额。例如：

```dotenv
COMPOSE_FILE=compose.release.yaml:compose.tls.yaml:compose.egress.yaml
```

```bash
docker compose up -d --wait --wait-timeout 120
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
```

未安装宿主服务或保护状态不可用时，Runner 拒绝新的联网操作。

## 3. 后台配置

超级管理员进入“网络设置”：

1. 选择服务器连接局域网的网卡。
2. NAT 虚拟机按需补充宿主外部的家庭 / 公司网段。
3. 需要例外访问时，在高级设置添加单个 IP 和端口。
4. 核对配置，确认保存。

补充网段可输入 IPv4 和掩码，或粘贴 ipconfig 并选择网卡。只在浏览器中识别地址与掩码，原文不提交；DNS 和网关不用于猜测网段。IPv6 额外网段在高级设置填写实际前缀。

容器使用配置的公共 DNS，避免依赖被拦截的路由器 DNS。最多 3 个；改变 DNS 后重新部署已有容器，其余规则保存后自动生效。

## 已有应用切换

Docker 网络的 internal 属性不能原地修改。首次启用或停用联网模式，需要在维护窗口重建本实例应用：

1. 备份，停止 API，用旧 Runner 的 `apps-freeze` 冻结运行应用。
2. 冻结成功后停止并移除旧 Runner 容器；它也连接着应用网络。核对实例标签，移除对应应用容器，确认网络没有剩余连接后再移除私有网络；保留镜像、数据库和 `apps/{UUID}/data`，不删除命名卷。
3. 更新宿主保护与 Compose，启动 Runner。
4. 执行 `apps-thaw`，验证数据、停启状态和配额，再启动 API。

不要对仍在联网的应用直接撤销保护。迁移到新服务器时，按新网卡和局域网重新配置。

## 排查

```bash
systemctl status stacknest-egress --no-pager
sudo journalctl -u stacknest-egress -n 50 --no-pager
sudo /usr/local/bin/stacknest-egress check
sudo nft list table inet stacknest_egress
```

配置文件为 `/etc/stacknest-egress/config.json`；服务只维护自己的 nft 表，不切换 Docker 防火墙后端。详细模块和控制边界见 [架构](ARCHITECTURE.md#联网保护)。
