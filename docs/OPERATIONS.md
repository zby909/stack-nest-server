# 日常运维

在实际部署目录运行，保留原项目名和入口、配额、联网覆盖。迁移后的私有配置使用 `docker compose -p stacknest -f /实际目录/compose.json` 替换简写命令。

## 1. 状态与排障

```bash
docker compose ps -a
docker compose logs --tail=100 stacknest runner nginx
docker compose exec -T nginx nginx -t
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
```

`migrate` 为 Exited (0) 正常。API 探针只验证数据库连接，unhealthy 不会自行触发 Docker 重启。doctor 进一步检查角色、SMTP 认证和可选依赖，不发邮件，不能验证实际送达。

| 现象                           | 下一步                                             |
| ------------------------------ | -------------------------------------------------- |
| 找不到 `../web`                | 仓库并排，版本匹配                                 |
| `migrate` 失败                 | 查看迁移日志，修复后重建一次性容器                 |
| 改环境变量仍不生效             | 重建相关服务，restart 不读取新环境或构建代码       |
| 改 PostgreSQL 初始化密码仍失败 | 已有密码不随初始化变量改变，须按数据库密码流程处理 |
| 登录邮件未到                   | 邮箱名单、SMTP 授权码、日志和垃圾邮件箱            |
| 登录后写操作 403               | 同源 API，代理保留原 Host 与真实 HTTPS 协议        |
| 静态项目 404                   | DNS、slug、正式发布及目录；草稿用独立预览地址      |
| 上传 413 / 504                 | 每层代理的大小、缓冲和超时                         |
| 上传 507 / 503                 | 达到 90%、预算不足，或磁盘探测不可用               |
| 应用访问失败                   | 失败阶段、HTTP 就绪、OOM、运行日志                 |
| CSS 仍旧                       | Cloudflare 尊重源站头，清理已有覆盖缓存            |
| API 重建后偶发 502             | 重启 Nginx，重新解析后端地址                       |

排障优先提供请求编号和脱敏结果，不公开授权码、配置或完整容器 inspect。

## 2. 文件与日志

`sites`、`previous`、`drafts` 保存当前、上一正式版和一个草稿。预览以数据库保留状态为准。发布切换可能短暂失败；暂停覆盖页面及资源，更新不解除暂停。

`pending`、`changes`、`trash` 是恢复依据，不能为释放 slug 手动删除。启动时按数据库完成或撤销未完成操作；无法确认则保留现场、拒绝启动。`trash` 不是用户回收站。

续传在 `uploads` 保留 24 小时，重启可继续，备份和迁移不携带未完成上传。归档 90 MiB、解压 200 MiB、最多 5000 条目，磁盘达到 90% 或预算不足时暂停上传。

操作日志每份约 1 MiB，保留当前和两份轮转；平台 Docker 日志 10 MiB × 3，应用日志 5 MiB × 2。界面查询的是保留记录，不是无限历史。

## 3. 备份

宿主需要 Bash、Python 3、Compose、flock、tar。首次备份或完成升级后，保存**实际已启动配置**，完整保留入口及可选覆盖：

```bash
python3 deploy/resolve_compose.py compose.resolved.json
```

工具读取当前目录的 Compose 选择，不回显配置。成功原子替换，Linux 权限 0600；失败保留旧快照。自定义参数照原启动方式指定：

```bash
python3 deploy/resolve_compose.py compose.resolved.json --project stacknest \
  --file compose.yaml --file compose.tls.yaml --file compose.quota.yaml
```

如果启动时使用了 `--env-file deployment.env`，保存快照时也加上相同的 `--env-file deployment.env`；多个环境文件按原顺序重复指定。快照包含解析后的密码，应保存在私有目录，不能提交到仓库或公开分享。

迁移实例已有完整私有 `compose.json`，直接交给备份工具即可。改配置前停用定时器，部署、自检及更新快照后再恢复。

手动备份，替换项目名和目录：

```bash
sudo env STACKNEST_COMPOSE_PROJECT=stacknest \
  bash deploy/backup.sh compose.resolved.json /opt/stacknest/backups "$PWD/backup-status"
```

第三参数与 API 挂载的 `STACKNEST_BACKUP_STATUS_DIR` 对应。脚本停 API、冻结运行应用，保存同一时点的 `postgres.dump` 和 `files.tar.gz`，校验可读性、写 SHA256SUMS，再恢复原状态。静态正式文件仍由 Nginx 读取。

默认保留 14 份完整结果；`.partial-` 是失败现场，不能作为完整备份。日常备份不保存镜像、环境配置或宿主服务；整体携带使用 [迁移包](MIGRATION.md)。

备份是私有文件。`backup-status/status.json` 只含时间、阶段、结果和体积，目录 0755、文件 0644，API 仅只读挂摘要。超过 48 小时未成功、运行超过 45 分钟和摘要损坏分别提示。

### 每日备份

模板 `deploy/stacknest-backup.service` 默认脚本目录为 `/opt/stacknest/current/server`。安装后用下面的 full edit **将 ExecStart 的脚本路径改为实际 server 目录**，保留环境参数。创建 `/etc/stacknest-backup.conf`，替换示例路径：

```dotenv
STACKNEST_BACKUP_COMPOSE=/实际server目录/compose.resolved.json
STACKNEST_BACKUP_ROOT=/opt/stacknest/backups
STACKNEST_BACKUP_STATUS_DIR=/实际server目录/backup-status
STACKNEST_COMPOSE_PROJECT=stacknest
STACKNEST_BACKUP_KEEP=14
```

```bash
sudo chmod 600 /etc/stacknest-backup.conf
sudo install -m 644 deploy/stacknest-backup.service deploy/stacknest-backup.timer /etc/systemd/system/
sudo systemctl edit --full stacknest-backup.service
sudo systemctl daemon-reload
sudo systemctl start stacknest-backup.service
sudo systemctl enable --now stacknest-backup.timer
systemctl list-timers stacknest-backup.timer
sudo journalctl -u stacknest-backup.service -n 50 --no-pager
```

先确认手动任务成功、摘要可见，再启用定时器。按服务器时区每天 03:15 加最多 5 分钟随机延迟运行；当前不发自动告警邮件。同机备份仍随服务器存储存在，换机前搬走所需数据。

## 4. 升级

准备匹配版本，先构建，再备份；维护窗口停止旧入口、API 和 Runner，避免旧程序在迁移后的 Schema 上继续写入：

```bash
docker compose build
```

构建与备份成功后：

```bash
(
  set -euo pipefail
  docker compose stop nginx stacknest runner
  docker compose rm -f migrate
  docker compose up -d
  docker compose ps -a
  docker compose exec -T nginx nginx -t
  docker compose exec -T stacknest /usr/local/bin/stacknest doctor
)
```

只删除一次性迁移容器，不删除卷。迁移失败保持 API 停止，修复再启动，不绕过迁移。此流程短暂停机。平台升级不等于回退用户应用的业务数据。

仅改前端可 `docker compose up -d --build --no-deps nginx`，再验收配置及页面。预构建模式先加载新镜像并更新版本，不执行源码 build。升级后核对项目、版本、应用、暂停和 HTTPS，并用 `resolve_compose.py` 更新备份快照。

## 5. 恢复与回退

数据库、文件、镜像和配置须属于同一批版本。换回旧容器不会回退 Schema、SQLite 或其他持久化业务数据。

整体恢复优先准备 [迁移包](MIGRATION.md)，在空白目标导入，SSH 验收后切换。工具不覆盖原实例；同机演练须用新目录、项目名，并避免激活冲突端口。

日常备份的原地恢复示例仅适用于**没有 Docker 应用的静态实例及其对应备份**。先备份当前目标，准备匹配版本和 PostgreSQL 17；任一步失败都保持 API、入口停止。

```bash
(
  set -euo pipefail
  stacknest_backup_dir=/实际备份目录
  (cd "$stacknest_backup_dir" && sha256sum -c SHA256SUMS)
  tar -tzf "$stacknest_backup_dir/files.tar.gz" > /dev/null
  test "$(docker compose exec -T postgres psql -U stacknest -d stacknest -Atc 'SELECT count(*) FROM container_apps;')" = 0
  docker compose stop nginx stacknest runner
  docker compose up -d --wait postgres
  docker compose exec -T postgres pg_restore -U stacknest -d stacknest \
    --clean --if-exists --no-owner --no-privileges --single-transaction \
    < "$stacknest_backup_dir/postgres.dump"
  test "$(docker compose exec -T postgres psql -U stacknest -d stacknest -Atc 'SELECT count(*) FROM container_apps;')" = 0
  docker compose run --rm --no-deps -T --user 0:0 --entrypoint sh stacknest \
    -ec 'rm -rf /data/sites /data/trash /data/pending /data/logs /data/previous /data/changes /data/paused /data/drafts; tar -C /data -xzf -' \
    < "$stacknest_backup_dir/files.tar.gz"
  docker compose rm -f migrate
  docker compose up -d
)
```

旧双 Nginx 布局同时停旧入口。数据库与文件不是共同事务，失败后保留现场并从同批备份重做；含应用的实例不能套用上述静态恢复命令。

## 6. 检查工具

开发回归见 [Server README](../README.md)。Linux 部署工具：

```bash
python3 -m unittest discover -s deploy -p 'test_*.py'
python3 deploy/check_nginx.py --image "$(docker compose images -q nginx)"
```

第一项验证控制流，Compose 解析无需 daemon。第二项需要 Linux、Docker、Python、OpenSSL 和 Web 镜像，用临时网络验证 HTTP / TLS、缓存、Host、SSH 和 Tunnel，再清理，不读写现有业务数据。

真实邮件、DNS、恢复演练和手机 Safari 仍需目标环境验收，标准见 [需求文档](REQUIREMENTS.md)。
