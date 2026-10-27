# 运维

命令在实际部署目录执行，保持原 Compose 项目名、入口和可选覆盖。使用私有 compose.json 的实例，将简写命令换成自己的 `docker compose -p 项目名 -f 文件`。

## 状态与排障

```bash
docker compose ps -a
docker compose logs --tail=100 stacknest runner nginx
docker compose exec -T nginx nginx -t
docker compose exec -T stacknest /usr/local/bin/stacknest doctor
```

`migrate` 为 Exited (0) 正常。超管可在“服务器状态 → 平台诊断”手动执行同一检查，最多 30 秒，不发送邮件；普通管理员只查看资源、备份和版本。

“平台版本”显示管理界面与 API 的版本和提交号。发布版不一致时先刷新浏览器，再核对是否使用了同一发布包。CLI 也可执行 `docker compose exec -T stacknest /usr/local/bin/stacknest version`。

| 问题             | 优先检查                                |
| ---------------- | --------------------------------------- |
| 迁移失败         | migrate 日志，修复后重建一次性容器      |
| 配置没生效       | 更新容器，restart 不读取新环境          |
| 收不到验证码     | 邮箱名单、SMTP 授权码、日志、垃圾邮件箱 |
| 登录后写接口 403 | 同源 API、Host、真实 HTTPS 协议         |
| 静态站点 404     | 域名、正式发布状态及目录                |
| 上传 413 / 504   | 代理大小限制、缓冲和超时                |
| 上传 507 / 503   | 磁盘达到 90%、空间预算或探测失败        |
| 应用访问失败     | 失败阶段、健康检查、内存与运行日志      |
| CSS 未更新       | Cloudflare 尊重源站标头并清理旧缓存     |
| API 重建后 502   | 重启 Nginx，重新解析后端地址            |

提交问题时提供请求编号和脱敏日志，不附密码或完整配置。

## 备份

宿主需要 Python 3、Bash、Compose、flock 和 tar。先保存实际运行配置，再手动备份：

```bash
python3 deploy/resolve_compose.py compose.resolved.json
sudo env STACKNEST_COMPOSE_PROJECT=stacknest bash deploy/backup.sh \
  compose.resolved.json "$PWD/backups" "$PWD/backup-status"
```

项目名换为自己的名称；摘要目录对应 `.env` 的 `STACKNEST_BACKUP_STATUS_DIR`。自定义启动参数时，resolve_compose.py 使用相同的 `--file`、`--env-file` 和 `--project`。

配置快照含密码，私有保存。备份包括同一时点的数据库和业务文件，默认保留 14 份；备份期间 API 与运行应用短暂停止，然后恢复原状态。

`.partial-` 是失败现场，不作为完整备份。日常备份不带镜像和运行配置；完整换机使用 [迁移包](MIGRATION.md)。恢复方法见 [恢复参考](RESTORE.md)。

### 每日备份

创建 `/etc/stacknest-backup.conf`，替换路径：

```dotenv
STACKNEST_BACKUP_COMPOSE=/实际server目录/compose.resolved.json
STACKNEST_BACKUP_ROOT=/实际备份目录
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
```

full edit 中将 ExecStart 的脚本路径改为实际 server 目录。先确认手动任务成功，再启用定时器；默认按服务器时区每日 03:15 运行，带最多 5 分钟随机延迟。

## 升级

1. 在旁边解压并校验新部署包，阅读版本说明。
2. 保留原项目名、密码、认证密钥、证书和自定义覆盖。
3. 同步新 Compose 与 deploy 文件，对照模板整组更新三个 IMAGE 及版本，合并新增配置。
4. 执行 `docker compose config --quiet`、`docker compose pull`，确认成功后备份。
5. 在维护窗口执行：

```bash
(
  set -euo pipefail
  docker compose stop nginx stacknest runner
  docker compose rm -f migrate
  docker compose up -d --wait --wait-timeout 120
  docker compose exec -T nginx nginx -t
  docker compose exec -T stacknest /usr/local/bin/stacknest doctor
)
```

仅改版本号不会更新固定摘要。源码部署先 build；私有 compose.json 实例直接更新被实际使用的配置。

不删除数据卷。迁移失败保持 API 停止，排查后再启动。升级后核对登录、项目和应用，并更新备份配置快照。已配置定时器时，修改配置前暂停，完成后恢复。

## 数据与日志

- sites、previous、drafts：当前、上一正式版与待发布版。
- pending、changes、trash：事务恢复依据，不手动清理；不是用户回收站。
- uploads：续传临时数据，保留 24 小时，不随备份和迁移携带。
- 操作日志：每份约 1 MiB，保留当前和两份轮转。
- 平台容器日志：10 MiB × 3；应用日志：5 MiB × 2。

[原理](ARCHITECTURE.md#数据备份与升级) · [换服务器](MIGRATION.md) · [恢复参考](RESTORE.md)
