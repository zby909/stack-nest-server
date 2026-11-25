# 恢复参考

完整恢复优先将 [迁移包](MIGRATION.md)导入空白实例。迁移包包含镜像、配置、数据库及业务文件。

日常备份保存数据库和业务文件，配套的私有配置按编号保存在备份根目录的 `.configs/`。复制整个备份目录时请包含这个隐藏目录。

## 演练日常备份

在 Linux 宿主准备备份时使用的平台镜像。用源码部署时，演练会查找原 Compose 项目构建的本地镜像；重新构建过同名镜像的实例，需另行准备对应版本。演练目录和 Compose 项目名须是新的。

```bash
sudo python3 deploy/restore_backup.py \
  /备份目录/stacknest-时间 /opt/stacknest-restore-test --project stacknest-restore-test
```

工具会先校验数据摘要和配套配置，再创建演练实例。旧备份（`stacknest-backup-v1`）需要在备份目录和演练目录之间加上当时的私有 Compose 文件路径。新备份也可这样指定另存的配置，编号必须匹配。平台版本记录在 `metadata` 的 `platform_version`，自建镜像没有版本标签时为 `unknown`。

恢复后工具检查数据库迁移和本地镜像。演练仅开放回环入口，默认不发送验证码，Docker 应用保持停止。

在自己的电脑运行 `ssh -N -L 19080:127.0.0.1:19080 用户@服务器`。打开 `http://localhost:19080/api/health` 核对健康状态，用 `http://stack-nest-项目标识.localhost:19080` 检查已发布站点。结束后按脚本提示清理演练容器和卷，再移除演练目录里的私有配置。

## 含 Docker 应用的实例

恢复需要同时核对应用固定镜像、加密配置、数据目录和应运行状态。使用完整迁移包在空白目标验收，再切换入口；不要将下方静态实例的原地恢复命令套用到含应用的备份。

## 仅静态实例的原地恢复

先用要恢复的那份备份完成上述演练，并另备份当前实例。在正式部署目录执行，确认演练库和正式库均无 Docker 应用后才覆盖数据库。恢复开始后若有步骤失败，保持 API 与入口停止。

```bash
(
  set -euo pipefail
  stacknest_backup_dir=/实际备份目录
  (cd "$stacknest_backup_dir" && sha256sum -c SHA256SUMS)
  tar -tzf "$stacknest_backup_dir/files.tar.gz" > /dev/null
  test "$(sudo docker compose -p stacknest-restore-test -f /opt/stacknest-restore-test/compose.json \
    exec -T postgres psql -U stacknest -d stacknest -Atc 'SELECT count(*) FROM container_apps;')" = 0
  docker compose stop nginx stacknest runner
  docker compose up -d --wait postgres
  test "$(docker compose exec -T postgres psql -U stacknest -d stacknest -Atc 'SELECT count(*) FROM container_apps;')" = 0
  docker compose exec -T postgres pg_restore -U stacknest -d stacknest \
    --clean --if-exists --no-owner --no-privileges --single-transaction \
    < "$stacknest_backup_dir/postgres.dump"
  test "$(docker compose exec -T postgres psql -U stacknest -d stacknest -Atc 'SELECT count(*) FROM container_apps;')" = 0
  docker compose run --rm --no-deps -T --user 0:0 --entrypoint sh stacknest \
    -ec 'cd /data
         rm -rf sites trash pending logs previous changes paused drafts uploads
         rm -f traffic-counts.json traffic-routes.json
         tar -xzf -' \
    < "$stacknest_backup_dir/files.tar.gz"
  docker compose rm -f migrate
  docker compose up -d --wait --wait-timeout 120
)
```

恢复会清除未完成的上传，用户需要重新选择文件。备份含访问统计快照时，计数从该快照续算。路由按恢复后的项目重建。启动后核对用户、版本、站点和暂停状态。若中途失败，保留现场，从同批备份重做。
