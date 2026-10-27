# 换服务器

迁移工具将镜像、数据库、业务文件、配置和证书导出，在空白目标恢复。入口先保持本地预览，验证后切换。

## 准备

- 两端 Linux 与 CPU 架构一致，Docker 镜像存储类型一致。
- 安装 Docker Compose 2.24.4+、Python 3.9+、Bash、tar 和 flock。
- 目标有足够空间，目标目录与同名容器、数据卷尚不存在。
- 已启用的 [配额](STORAGE_QUOTAS.md)和 [联网保护](NETWORK_EGRESS.md)先在目标准备。

磁盘挂载、Docker daemon 配置、宿主联网服务和备份定时器不随业务包自动安装。导入前核对目标局域网、VPN、Docker 地址池与包内平台网段不重叠。

迁移包含密码和私钥，私有保存并通过 SSH / SCP 传输。以下示例使用 `stacknest` 项目名和 root SSH，按实际用户、路径替换。

## 1. 导出

在旧部署目录保存完整配置。已配置每日备份时，先执行 `sudo systemctl disable --now stacknest-backup.timer` 停用定时器，再冻结导出：

```bash
(
  set -euo pipefail
  python3 deploy/resolve_compose.py compose.resolved.json
  sudo python3 deploy/migrate_host.py export compose.resolved.json \
    /opt/stacknest/migration-final --project stacknest --freeze
  sudo python3 deploy/migrate_host.py check /opt/stacknest/migration-final
)
```

私有 compose.json 实例直接用该文件导出。

导出目录必须是新的。`--freeze` 成功后保持 API 与运行应用停止，防止最终快照后再产生写入。仅演练时省略此参数，导出后自动恢复原状态。

## 2. 传输与导入

在旧服务器：

```bash
sudo scp -r /opt/stacknest/migration-final root@NEW_SERVER_IP:/root/
```

导出目录归 root 所有、权限 0700，因此普通账户传输时使用 sudo。目标 SSH 用户和端口按实际情况调整。

若包内启用了联网保护，**导入前**先用包内 `deploy/stacknest-egress` 安装服务并检查就绪。目标 LAN 配置按新机器确定，具体见 [应用联网](NETWORK_EGRESS.md)。

在新服务器：

```bash
sudo python3 /root/migration-final/deploy/migrate_host.py import \
  /root/migration-final /opt/stacknest-migrated --project stacknest
```

成功显示 import_complete，仅开放回环预览。失败时保留数据排查，重试使用新的目标目录和项目名。

## 3. 验证

在自己的电脑执行：

```bash
ssh -N -L 18081:127.0.0.1:18080 root@NEW_SERVER_IP
```

打开 `http://localhost:18081`，核对成员、日志、静态版本、暂停状态，以及 Docker 应用数据和启停状态。项目使用界面返回的 `.localhost:18081` 地址。

未完成上传不迁移。验收期间避免同时向新旧实例写入。

## 4. 切换

在新服务器：

```bash
sudo python3 /opt/stacknest-migrated/deploy/migrate_host.py activate \
  /opt/stacknest-migrated --project stacknest
```

普通 HTTPS 切换 DNS 并检查证书。Tunnel 另配置目标 cloudflared 后切换连接器，Token 不随业务包迁移；只安装连接器，沿用已恢复的平台，不要再运行一套首次安装流程。

新实例稳定后再处理旧服务器。新实例尚未产生写入时，可在旧实例执行 Runner 的 apps-thaw、恢复 API 和备份定时器；新实例已有写入后，先迁回最新数据再回切。

## 后续管理

恢复配置位于 `/opt/stacknest-migrated/compose.json`：

```bash
sudo docker compose -p stacknest -f /opt/stacknest-migrated/compose.json ps
```

维护时沿用该配置；修改域名或 SMTP 同时核对 compose.json 与 compose.public.json。自动备份定时器在新机器重新安装。

自建镜像仓库及其他外部依赖不自动迁移。备份和维护见 [运维](OPERATIONS.md)，数据协作原理见 [架构](ARCHITECTURE.md#数据备份与升级)。
