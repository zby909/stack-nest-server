# Docker 磁盘硬配额

这是可选能力，普通 ext4 服务器可以运行平台，但不能据此宣称应用已有磁盘硬限额。硬配额要求 Linux 5.14+、XFS（ftype=1、prjquota）及传统 overlay2；Docker 根目录与平台数据卷须在同一 XFS。

## 1. 准备文件系统

使用已经正确挂载的 XFS 数据目录，例如 `/data`。先检查，不能为启用配额直接格式化正在使用的盘：

```bash
findmnt -no SOURCE,FSTYPE,OPTIONS /data
xfs_info /data
sudo xfs_quota -x -c 'state -p' /data
df -h / /data
```

应显示 XFS、ftype=1，Project quota 记账与执行均开启。`pquota` 和 `prjquota` 等价；它按项目编号统计磁盘用量，不由 cgroups 管理。

开机挂载使用该机器真实 UUID。下面是格式示例，**先替换 UUID**，不是可照抄的实例配置：

```fstab
UUID=实际数据盘UUID /data xfs defaults,noatime,prjquota,nofail,x-systemd.device-timeout=10s 0 0
```

Docker / containerd 应通过 `RequiresMountsFor=/data` 等待挂载，并核对文件系统和配额后启动，防止挂载失败时误用系统盘。业务迁移包不会替你安装这些宿主配置。

## 2. 配置 Docker

确认数据已备份，按实际环境合并 daemon 配置，保留原选项：

```json
{
  "data-root": "/data/docker",
  "storage-driver": "overlay2",
  "features": { "containerd-snapshotter": false },
  "storage-opts": ["overlay2.size=1G"]
}
```

这是传统镜像存储的示例。已有 Docker 业务不能直接靠改目录切换，先完成存储迁移。Docker 29+ 空白安装可能默认 containerd 镜像存储，切换会影响固定镜像编号；见 [完整迁移](MIGRATION.md)。

`overlay2.size=1G` 成为其他新容器可写层的默认上限，命名卷不受它限制。上限不预留磁盘，不包括镜像只读层、命名卷内的数据库或宿主备份；应用若把数据库写在自身可写层，仍受该上限约束且重建后不保留。

## 3. 启用应用数据配额

设置实际 Docker 根目录和每应用数据目录上限，再把 `compose.quota.yaml` 加在已选入口后：

```dotenv
STACKNEST_DOCKER_STORAGE_DIR=/data/docker
STACKNEST_APP_DATA_LIMIT_MIB=1024
COMPOSE_FILE=compose.release.yaml:compose.tls.yaml:compose.quota.yaml
```

SSH / Tunnel 使用对应入口替换 TLS；源码部署将基础文件换为 compose.yaml。然后执行部署更新和自检：

```bash
docker compose up -d
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
```

该覆盖只给受信任 Runner 增加 SYS_ADMIN，不给 API 或用户应用。Runner 用目录描述符设置 XFS 配额，不挂宿主块设备。条件不满足时拒绝启动，不静默取消限制。

| 对象                 | 示例硬上限          | 管理方式            |
| -------------------- | ------------------- | ------------------- |
| 每容器可写层         | 1 GiB               | overlay2 size       |
| 每应用持久化数据目录 | 1 GiB、100000 inode | Runner XFS 项目配额 |
| 每应用 Docker 日志   | 5 MiB × 2           | 日志轮转            |

可写层和数据目录分别计量。更新时旧容器与候选各有可写层，共用一个持久化目录；更新、重启保留数据，永久删除时清理数据与配额。

应用可使用镜像 root USER；配额模式的 seccomp 阻止修改项目编号、继承标记和 chattr 属性的 ioctl，读取和其他 ioctl 保持原规则，防止去掉数据目录限额。

## 4. 已有实例及维护

已运行的不带保护容器不能只换配置就继续使用。先备份、停止 API、用旧 Runner 冻结应用，移除经过标签确认的本实例应用容器，保留数据库、镜像和 `apps/{UUID}/data`；启用配额后重新创建并验收。

值为空或 0 禁用持久化配额。配额编号位于 `apps/{UUID}/disk-quota.json`，由 Runner 管理，不人工改。超过容量或 inode 限额可显示空间不足，即使整盘仍有空闲。

```bash
sudo xfs_quota -x -c 'report -p -h' /data
sudo docker info --format '{{.Driver}} {{.DockerRootDir}}'
df -h / /data
```

tar 恢复不携带 XFS 项目属性，Runner 在应用启动前重新初始化配额。恢复数据超过目标上限时可能失败，应先核对实际用量和目标配置。

seccomp 基线维护使用 `deploy/generate_quota_seccomp.py`，核对官方摘要与差异后执行 Linux 容器回归；不在运行时自动下载、放宽规则。

参考：[Docker overlay2 容量](https://docs.docker.com/reference/cli/dockerd/#overlay2-options)、[每容器存储选项](https://docs.docker.com/reference/cli/docker/container/run/#set-storage-driver-options-per-container---storage-opt)、[XFS project quota](https://www.man7.org/linux/man-pages/man8/xfs_quota.8.html)。
