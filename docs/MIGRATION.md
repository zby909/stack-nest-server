# 换服务器：完整迁移 StackNest

用于本仓库的 Docker Compose 单实例（postgres、migrate、stacknest、nginx、runner）。其中 migrate 为一次性容器，其他四个常驻。兼容旧版独立 tunnel Nginx 及不含 Runner 的静态部署。迁移包包含固定版本镜像、数据库、业务文件、运行配置和所需证书；目标机器不必重新构建。

工具拒绝覆盖同名容器、数据卷或已有目录。恢复后只开放本机预览，验收后再开放公网。不自动改 DNS，也不删除旧服务器。本工具用于更换服务器，不做持续异地备份。

## 1. 准备

- 两台服务器均为 Linux，Docker 的系统与 CPU 架构一致，安装 Docker Engine、Compose 2.24.4+、Python 3.9+、Bash、tar 和 flock，以 root 或 sudo 执行。
- 目标磁盘应能容纳镜像、数据库、文件及临时开销。工具会预检，但实际空间仍取决于解压情况。
- 源和目标的 Docker 镜像存储类型需要一致：传统存储（当前服务器为 overlay2）或 containerd 镜像存储。新导出包会记录类型，导入时在创建目录、加载镜像前检查；跨类型迁移需要额外转换服务及应用的固定镜像编号，通用换机工具暂不自动转换。旧包没有此字段，需要人工确认。
- 当前 Compose 项目名为 `stacknest`，自定义安装应替换为真实名称。支持新版单 Nginx 五服务布局，以及旧版含 tunnel 的五 / 六服务布局，不包含附加业务服务或外部网络。
- 包内含 SMTP 凭据、会话密钥、数据库和证书私钥，只通过 SSH/SCP 传输，放在私有目录，不上传 GitHub 或网站。摘要用于检测损坏，只导入自己生成且可信的包。

## 2. 旧服务器导出

如果使用源码 Compose 和覆盖文件，先在实际启动目录生成一份私有解析配置，完整保留 HTTPS / Tunnel、配额及联网设置。不要只导出基础 compose.yaml：

```bash
(
  set -euo pipefail
  python3 deploy/resolve_compose.py compose.resolved.json
  sudo python3 deploy/migrate_host.py export compose.resolved.json ~/stacknest-migration --project stacknest
  sudo python3 deploy/migrate_host.py check ~/stacknest-migration
)
```

迁移后已有私有 `compose.json` 的实例，直接把该文件作为 export 参数；无需再次输出配置。导出目录必须不存在。以下命令中的 `/opt/stacknest/current/server/compose.resolved.json` 表示真实运行配置，应按实际路径替换。

```bash
sudo python3 /opt/stacknest/current/server/deploy/migrate_host.py export /opt/stacknest/current/server/compose.resolved.json /opt/stacknest/migration-20261008 --project stacknest
sudo python3 /opt/stacknest/current/server/deploy/migrate_host.py check /opt/stacknest/migration-20261008
```

工具先停止 API 和正在运行的应用，再保存固定镜像及同一时间点的数据库、静态文件和应用数据快照，最后恢复原运行状态。现有静态站点仍由 Nginx 提供。导出与每日备份共享实例锁，并发时会拒绝后一个任务；等待前一个任务完成再用新目录重试。成功显示 `export_complete` 和 `migration_bundle_verified=true`；失败时保留现场，未完成包不能导入。不要编辑包内文件，否则摘要校验会失败。

正式切换前先执行 `sudo systemctl disable --now stacknest-backup.timer`（若已配置），避免定时备份在冻结后恢复旧应用。再用新目录导出一次，并加 `--freeze`。成功后旧 API 及应用保持停止，防止导出后产生新上传、用户或日志；失败则恢复原 API。早前的演练包不是最终数据。

```bash
sudo python3 /opt/stacknest/current/server/deploy/migrate_host.py export /opt/stacknest/current/server/compose.resolved.json /opt/stacknest/migration-final --project stacknest --freeze
```

## 3. 传输并恢复

例如在旧服务器执行 SCP，替换新服务器地址：

```bash
scp -r /opt/stacknest/migration-final root@NEW_SERVER_IP:/root/
```

登录新服务器。若原实例启用了应用联网保护，先安装目标机的 `iproute2`、`nftables`，并使用包内程序安装规则服务，再导入业务数据：

```bash
sudo apt-get install -y iproute2 nftables
ip -br address
sudo /root/migration-final/deploy/stacknest-egress install --interface eth0
sudo /usr/local/bin/stacknest-egress check
```

将 `eth0` 换为新服务器真实 LAN 网卡；Hyper-V NAT 外的家庭网段需另加 `--block-cidr 实际网段`。网段配置按目标机器重新确定，详见 [联网保护说明](NETWORK_EGRESS.md)。未启用此功能的包没有该程序，无需执行这些命令。

导出包中该程序保留 0700 执行权限，其他文件为 0600；不要对包递归执行 `chmod 600`，否则程序无法运行。

目标目录必须不存在，`stacknest` 名下不能有旧容器或数据卷：

```bash
sudo python3 /root/migration-final/deploy/migrate_host.py import /root/migration-final /opt/stacknest-migrated --project stacknest
```

工具检查完整性、路径和磁盘，装载固定镜像，在空库和新数据卷恢复，然后启动、检查服务。成功显示 `import_complete`，仅 Nginx 的 SSH 预览端口绑定回环；新版使用容器内 82，旧版 tunnel 使用 80，HTTPS 和 Tunnel 回源均需验收后激活。失败时关闭新实例 API 和入口，保留数据排查；旧服务器数据不受影响。排查后重试应使用全新目录和项目名，不要手动覆盖绕过保护。

在自己电脑开 SSH 隧道验收：

```bash
ssh -N -L 18081:127.0.0.1:18080 root@NEW_SERVER_IP
```

打开 `http://localhost:18081/`，登录核对项目、用户、日志、当前版、上一版、待发布版及暂停状态。站点预览使用 `http://stack-nest-项目slug.localhost:18081/`。版本预览使用详情页返回的 `stack-nest--<UUID>.localhost:18081` 地址；编号与内容随迁移保留。未完成分片上传不包含在包内，需重新选择文件。最终切换验收期间不要同时向新旧实例写入。

仅演练时可另选项目名与 `--preview-port 18081` 来隔离原实例。同一宿主机上的演练实例不要执行下一节的 `activate`，以免冲突原有 80/443 端口。

## 4. 开放并切换

在新服务器验收后执行：

```bash
sudo python3 /opt/stacknest-migrated/deploy/migrate_host.py activate /opt/stacknest-migrated --project stacknest
```

此命令恢复导出时的入口映射，失败会恢复本机预览。普通 HTTPS 部署随后切换 DNS 到新 IP，检查证书和 80/443；Cloudflare 普通代理还检查 Full (strict) 与源站规则。Tunnel 部署恢复回环 18081 后，另配置新主机的 cloudflared，再切换连接器；不会替你安装或启动它。

新实例稳定后再处置旧服务器，建议保留旧数据和迁移包直到验收完成。新实例尚未写入时，可在旧服务器先用原 Compose 执行 `exec -T runner /usr/local/bin/stacknest apps-thaw`（启用 Docker 应用时），再执行 `up -d --no-deps --no-recreate --wait stacknest` 取消冻结，并重新启用旧备份定时器；新实例已经写入后，需先迁回最新数据，不能直接启动旧数据继续使用。

## 5. 后续管理

迁移后的配置为 `/opt/stacknest-migrated/compose.json`，不再依赖旧目录的 `.env`。镜像固定为导出时的 ID，不自动升级。修改 SMTP 或域名时同步维护 `compose.json` 与 `compose.public.json`，保持私有权限，不打印完整配置。

```bash
sudo docker compose -p stacknest -f /opt/stacknest-migrated/compose.json ps
sudo docker compose -p stacknest -f /opt/stacknest-migrated/compose.json logs --tail=50 stacknest
sudo env STACKNEST_COMPOSE_PROJECT=stacknest bash /opt/stacknest-migrated/deploy/backup.sh /opt/stacknest-migrated/compose.json /opt/stacknest-migrated/backups /opt/stacknest-migrated/backup-status
```

旧服务器的 systemd 定时任务不会随包迁移。若继续每日自动备份，按 [日常运维](OPERATIONS.md) 安装 service/timer，并把 service 的脚本路径改为 `/opt/stacknest-migrated/deploy/backup.sh`，配置文件中的 Compose、备份根目录与摘要目录换为上面路径，项目名保持一致。先手动执行一次并在服务器状态页确认成功，再启用定时器。

包内包含运行镜像，不包含完整源码仓库；代码仍保存在本机与原 Git 仓库。后续升级应使用匹配的后端、前端和数据库迁移，不能只替换其中一张镜像。

Docker 应用验收应核对应用域名、数据目录和停启状态。固定镜像随包迁移，只有显式更新才重新访问仓库；数据目录内的越界 / 绝对链接及设备文件会拒绝迁移。新服务器的 Docker 存储根目录由工具自动读取并替换，运行器控制卷不打包。

## 迁移前确认存储与硬配额

业务包不会创建文件系统、Docker 存储目录或项目配额。先按 [硬配额说明](STORAGE_QUOTAS.md)完成目标机检查；实际实例信息记录在自己的私有台账，不照抄历史磁盘 UUID。

空白 Docker 29+ 默认可能使用 containerd 镜像存储，当前包要求传统镜像存储。空白目标应先合并 `features.containerd-snapshotter=false`、`storage-driver=overlay2`，重启检查再导入；不要用于切换已运行其他业务的服务器。

文件系统、挂载和 Docker 默认限额不随业务包配置。tar 恢复后的项目属性由新 Runner 初始化。从未启用硬配额的实例迁来，先以原模式导入，再停止 API 和 Runner、停止并移除本实例的应用容器，保留 apps 数据目录；换成配额版本配置后启动 Runner，再恢复应用及 API。直接对旧容器开启新保护会被 Runner 拒绝，不会静默放宽限制。

本次另行迁移了 Compose 之外的本地演示镜像仓库，并验证更新拉取。通用工具不会自动迁移自建仓库、宿主定时任务及其他外部依赖。

## 应用联网保护

启用应用公网出站的实例，迁移包会携带网络规则服务文件。新主机须先按自己的局域网安装服务，导入前会检查其就绪状态；旧机器的 LAN 网段不会自动沿用。Hyper-V NAT 后面的家庭网络需配置额外网段。见 [联网与局域网保护](NETWORK_EGRESS.md)。
