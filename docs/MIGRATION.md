# 换服务器：完整迁移 StackNest

用于当前 `compose.release.yaml` 的五个基础服务（postgres、migrate、stacknest、nginx、tunnel）及可选 Docker Runner。迁移包包含固定版本的 Docker 镜像、数据库、当前与上一正式版、待发布版及说明、暂停状态、操作日志、运行配置和源站证书，新服务器不用拉取镜像或重新构建前端。

工具拒绝覆盖同名容器、数据卷或已有目录。恢复后只开放本机预览，验收后再开放公网。不自动改 DNS，也不删除旧服务器。本工具用于更换服务器，不做持续异地备份。

## 1. 准备

- 两台服务器均为 Linux，Docker 的系统与 CPU 架构一致，安装 Docker Engine、Compose v2、Python 3.9+、Bash、tar 和 flock，以 root 或 sudo 执行。
- 目标磁盘应能容纳镜像、数据库、文件及临时开销。工具会预检，但实际空间仍取决于解压情况。
- 源和目标的 Docker 镜像存储类型需要一致：传统存储（当前服务器为 overlay2）或 containerd 镜像存储。新导出包会记录类型，导入时在创建目录、加载镜像前检查；跨类型迁移需要额外转换服务及应用的固定镜像编号，通用换机工具暂不自动转换。旧包没有此字段，需要人工确认。
- 当前 Compose 项目名为 `stacknest`，自定义安装应替换为真实名称。支持本仓库的基础发布布局及增加 runner 的六服务布局，不包含附加业务服务或外部网络。
- 包内含 SMTP 凭据、会话密钥、数据库和证书私钥，只通过 SSH/SCP 传输，放在私有目录，不上传 GitHub 或网站。摘要用于检测损坏，只导入自己生成且可信的包。

## 2. 旧服务器导出

先登录旧服务器。导出目录必须不存在，可按实际日期取名：

```bash
sudo python3 /opt/stacknest/current/server/deploy/migrate_host.py export /opt/stacknest/current/server/compose.release.yaml /opt/stacknest/migration-20261008 --project stacknest
sudo python3 /opt/stacknest/current/server/deploy/migrate_host.py check /opt/stacknest/migration-20261008
```

工具先停止 API 和正在运行的应用，再保存固定镜像及同一时间点的数据库、静态文件和应用数据快照，最后恢复原运行状态。现有静态站点仍由 Nginx 提供。导出与每日备份共享实例锁，并发时会拒绝后一个任务；等待前一个任务完成再用新目录重试。成功显示 `export_complete` 和 `migration_bundle_verified=true`；失败时保留现场，未完成包不能导入。不要编辑包内文件，否则摘要校验会失败。

正式切换前先执行 `sudo systemctl disable --now stacknest-backup.timer`（若已配置），避免定时备份在冻结后恢复旧应用。再用新目录导出一次，并加 `--freeze`。成功后旧 API 及应用保持停止，防止导出后产生新上传、用户或日志；失败则恢复原 API。早前的演练包不是最终数据。

```bash
sudo python3 /opt/stacknest/current/server/deploy/migrate_host.py export /opt/stacknest/current/server/compose.release.yaml /opt/stacknest/migration-final --project stacknest --freeze
```

## 3. 传输并恢复

例如在旧服务器执行 SCP，替换新服务器地址：

```bash
scp -r /opt/stacknest/migration-final root@NEW_SERVER_IP:/root/
```

登录新服务器。目标目录必须不存在，`stacknest` 名下不能有旧容器或数据卷：

```bash
sudo python3 /root/migration-final/deploy/migrate_host.py import /root/migration-final /opt/stacknest-migrated --project stacknest
```

工具检查完整性、路径和磁盘，装载固定镜像，在空库和新数据卷恢复，然后启动、检查服务。成功显示 `import_complete`，公网端口仍未绑定。失败时关闭新实例 API 和入口，保留数据排查；旧服务器数据不受影响。排查后重试应使用全新目录和项目名，不要手动覆盖绕过保护。

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

此命令恢复原有公网端口，失败会恢复本机预览配置。之后将管理域名与项目泛域名 A 记录改到新服务器，核对 Cloudflare 源站端口、Full (strict)、证书与安全组，再实测域名。

新实例稳定后再处置旧服务器，建议保留旧数据和迁移包直到验收完成。新实例尚未写入时，可在旧服务器先用原 Compose 执行 `exec -T runner /usr/local/bin/stacknest apps-thaw`（启用 Docker 应用时），再执行 `up -d --no-deps --no-recreate --wait stacknest` 取消冻结，并重新启用旧备份定时器；新实例已经写入后，需先迁回最新数据，不能直接启动旧数据继续使用。

## 5. 后续管理

迁移后的配置为 `/opt/stacknest-migrated/compose.json`，不再依赖旧目录的 `.env`。镜像固定为导出时的 ID，不自动升级。修改 SMTP 或域名时同步维护 `compose.json` 与 `compose.public.json`，保持私有权限，不打印完整配置。

```bash
sudo docker compose -p stacknest -f /opt/stacknest-migrated/compose.json ps
sudo docker compose -p stacknest -f /opt/stacknest-migrated/compose.json logs --tail=50 stacknest
sudo env STACKNEST_COMPOSE_PROJECT=stacknest bash /opt/stacknest-migrated/deploy/backup.sh /opt/stacknest-migrated/compose.json /opt/stacknest-migrated/backups /opt/stacknest-migrated/backup-status
```

旧服务器的 systemd 定时任务不会随包迁移。若继续每日自动备份，按 [部署文档](DEPLOYMENT.md) 安装 service/timer，并把 service 的脚本路径改为 `/opt/stacknest-migrated/deploy/backup.sh`，配置文件中的 Compose、备份根目录与摘要目录换为上面路径，项目名保持一致。先手动执行一次并在服务器状态页确认成功，再启用定时器。

包内包含运行镜像，不包含完整源码仓库；代码仍保存在本机与原 Git 仓库。后续升级应使用匹配的后端、前端和数据库迁移，不能只替换其中一张镜像。


Docker 应用验收应核对应用域名、数据目录和停启状态。固定镜像随包迁移，只有显式更新才重新访问仓库；数据目录内的越界 / 绝对链接及设备文件会拒绝迁移。新服务器的 Docker 存储根目录由工具自动读取并替换，运行器控制卷不打包。


## 当前安装与磁盘配额

2026-10-10 已迁至 `zby909@100.68.110.44`（Tailscale 内网）。独立 30 GiB XFS 数据盘挂载 `/data`，Docker 位于 `/data/docker`，containerd 位于 `/data/containerd`，使用传统 overlay2。应用可写层和持久化目录分别为 1 GiB 硬上限，见 [磁盘配额说明](STORAGE_QUOTAS.md)。

运行配置 `/data/stacknest/runtime/compose.json`，公网备用配置同目录 `compose.public.json`，包含凭据，保持 0600。每日备份 `/data/stacknest/backups`，北京时间 03:15 加至多 5 分钟随机延迟，保留 14 份。摘要目录 `/data/stacknest/runtime/backup-status` 供后台只读访问。

```bash
ssh -N -L 18080:127.0.0.1:18080 zby909@100.68.110.44
```

当前回环地址 18080 供 SSH 访问，18081 供原生 cloudflared 回源。公网已通过 Cloudflare Tunnel 提供 HTTPS，见 [隧道配置](CLOUDFLARE_TUNNEL.md)。Tailscale 的 100.x 地址不能作为公网入口 A 记录。旧后台和应用冻结，数据和私有迁移包保留。

空白 Docker 29+ 默认可能使用 containerd 镜像存储，当前包要求传统镜像存储。空白目标应先合并 `features.containerd-snapshotter=false`、`storage-driver=overlay2`，重启检查再导入；不要用于切换已运行其他业务的服务器。

文件系统、挂载和 Docker 默认限额不随业务包配置。tar 恢复后的项目属性由新 Runner 初始化。从未启用硬配额的实例迁来，先以原模式导入，再停止 API 和 Runner、停止并移除本实例的应用容器，保留 apps 数据目录；换成配额版本配置后启动 Runner，再恢复应用及 API。直接对旧容器开启新保护会被 Runner 拒绝，不会静默放宽限制。

本次另行迁移了 Compose 之外的本地演示镜像仓库，并验证更新拉取。通用工具不会自动迁移自建仓库、宿主定时任务及其他外部依赖。
