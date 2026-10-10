# Docker 磁盘硬配额与当前服务器

2026-10-10 已将 StackNest 迁至新 Ubuntu 虚拟机，整机重启验收通过。系统盘保持 ext4，独立 30 GiB 数据盘使用原生 XFS，不再依赖旧 ECS 的 XFS 虚拟磁盘文件。

## 当前配置

| 用途 | 配置 |
| --- | --- |
| 系统盘 | /dev/sda；保持原有分区和 ext4 |
| 数据盘 | /dev/sdb1；30 GiB，XFS，ftype=1 |
| 通用数据目录 | /data |
| 数据盘 UUID | 8dd4b367-8b4a-4929-aebf-f5618b60f4a9 |
| 项目配额 | prjquota；记账和执行均开启 |
| Docker 数据根目录 | /data/docker；传统 overlay2 |
| containerd 数据根目录 | /data/containerd |
| 运行配置 | /data/stacknest/runtime/compose.json |
| 每日备份 | /data/stacknest/backups |

`pquota`、`prjquota` 是等价的 XFS 项目配额选项。内核按文件所属项目编号累计磁盘用量，超限时拒绝写入；它与 cgroups 控制 CPU、内存、PID 的机制不同。

`/etc/fstab` 使用 UUID 自动挂载：

```fstab
UUID=8dd4b367-8b4a-4929-aebf-f5618b60f4a9 /data xfs defaults,noatime,prjquota,nofail,x-systemd.device-timeout=10s 0 0
```

Docker 和 containerd 的 systemd 配置通过 `RequiresMountsFor=/data` 等待挂载，并执行 `/usr/local/sbin/stacknest-storage-check`，核对 UUID、XFS、配额后才允许启动。挂载失败时不误用系统盘；SSH 和 Tailscale 不依赖此目录。

## 应用具体限制

| 对象 | 当前硬上限 | 管理方式 |
| --- | --- | --- |
| 每个应用容器的可写层 | 1 GiB | Docker overlay2 的 size=1G |
| 每应用持久化数据目录 | 1 GiB、100000 个 inode | Runner 设置 XFS 项目配额 |
| 每容器日志 | 5 MiB × 2 | Docker 日志轮转 |

持久化目录位于命名卷中的 `apps/<应用UUID>/data`，与可写层分别计量。更新期间旧容器和候选容器各有自己的可写层上限，共用一个持久化目录。重启和更新保留该目录，永久删除应用时清理数据及配额。

上限不预留空间，也不覆盖镜像只读层、数据库、备份、临时迁移包或整台服务器。镜像解压大小、磁盘 90% 使用率和空间预留检查继续保留；所有数据共用这块 30 GiB 数据盘。

应用沿用镜像的 USER，可以在容器内使用 root。配额模式附加基于 Moby 默认配置的 seccomp 策略，禁止修改项目编号、继承标记和 chattr 属性的 ioctl，防止 root 移除配额。读取属性和其他 ioctl 继续可用；需要修改这些属性的应用会收到权限错误。

## 在其他服务器启用

持久化配额需要 Linux amd64 / arm64、内核 5.14+。Docker 使用 XFS＋项目配额＋传统 overlay2；平台命名卷与 Docker 存储须在同一 XFS 文件系统。已有 Docker 存储切换须先备份和迁移，不能只改目录名。

合并以下相关字段到宿主 Docker 配置，保留原有日志等选项：

```json
{
  "data-root": "/data/docker",
  "storage-driver": "overlay2",
  "features": {"containerd-snapshotter": false},
  "storage-opts": ["overlay2.size=1G"]
}
```

`overlay2.size=1G` 也会成为其他新容器可写层的默认值，命名数据卷不受它限制。Runner 还需 `STACKNEST_DOCKER_STORAGE_DIR=/data/docker`。

```bash
docker compose -f compose.release.yaml -f compose.quota.yaml up -d
```

可选 `compose.quota.yaml` 仅向受信任 Runner 增加 `CAP_SYS_ADMIN`，设置 `STACKNEST_APP_DATA_LIMIT_MIB=1024`。后台 API 和用户应用不会获得这项 capability。Runner 使用目录文件描述符设置配额，不挂载宿主块设备。

值为空或 0 时禁用持久化配额；开启后文件系统、内核或权限不满足要求，Runner 拒绝启动，不能降级成不限额运行。已有不带保护策略的应用容器须先停止并重建，保留数据库和数据目录，不得通过删除数据卷切换。

## 检查与维护

```bash
findmnt -no SOURCE,FSTYPE,OPTIONS /data
xfs_info /data
sudo xfs_quota -x -c 'state -p' /data
sudo xfs_quota -x -c 'report -p -h' /data
sudo docker info --format '{{.Driver}} {{.DockerRootDir}}'
df -h / /data
sudo systemctl status docker containerd stacknest-backup.timer
sudo docker compose -p stacknest -f /data/stacknest/runtime/compose.json ps
```

配额编号保存在 `apps/<应用UUID>/disk-quota.json`，用户应用不能访问。由 Runner 管理，不要人工修改。超额写入可能显示“空间不足”，即使整块盘仍有空闲，也应检查应用自己的额度。

备份只归档数据库和业务文件，不复制整块磁盘。tar 恢复不带 XFS 项目属性，Runner 在应用启动前重新分配编号、初始化已恢复数据的配额。数据超过目标上限时，初始化或后续写入可能失败，应核对用量和统一上限。

升级 seccomp 基线时使用 `deploy/generate_quota_seccomp.py`，校验官方配置摘要并人工检查变化；不能在构建或运行时静默下载并放宽策略。修改后运行 Python 检查和 Linux 实际容器演练。

## 验收与回退

3 个用户、3 个静态项目、1 个 Docker 应用恢复成功，数据库、文件摘要、加密配置和演示计数数据一致。新服务通过 Tailscale＋SSH 隧道访问，公网 DNS 未修改。

实测通过 64 MiB 可写层和 32 MiB 数据目录超限拒写、应用互不影响、root 修改配额被拒绝、恢复和清理；生产额度为各 1 GiB。临时测试容器、数据、配额及传输密钥已清理。整机重启后自动挂载、配额执行、服务及备份定时器正常。

旧 ECS 数据及 16 GiB XFS 虚拟磁盘保留用于回退，旧后台和应用冻结，每日备份定时器停用。新服务器已有新写入后不能直接解冻旧实例，应先迁回最新数据。

## 参考

- [Docker overlay2 容量配置](https://docs.docker.com/reference/cli/dockerd/#overlay2-options)
- [Docker 每容器存储选项](https://docs.docker.com/reference/cli/docker/container/run/#set-storage-driver-options-per-container---storage-opt)
- [Linux quotactl 与 quotactl_fd](https://www.man7.org/linux/man-pages/man2/quotactl.2.html)
- [XFS 项目配额](https://www.man7.org/linux/man-pages/man8/xfs_quota.8.html)
