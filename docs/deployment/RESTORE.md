# 恢复参考

完整恢复优先将 [迁移包](MIGRATION.md)导入空白实例。迁移包包含镜像、配置、数据库及业务文件。

日常备份只保存数据库和业务文件。另存备份时的运行配置及平台版本，恢复时沿用其中的原认证密钥。

## 演练日常备份

在 Linux 宿主准备备份时的 [私有 Compose JSON](OPERATIONS.md#备份)及对应的本地平台镜像。演练目录和 Compose 项目名须是新的。

```bash
sudo python3 deploy/restore_backup.py \
  /备份目录/stacknest-时间 /配置目录/compose.resolved.json \
  /opt/stacknest-restore-test --project stacknest-restore-test
```

工具校验摘要、恢复数据库和文件，再检查迁移状态及本地镜像。演练仅开放回环入口，默认不发送验证码，Docker 应用保持停止。

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
    -ec 'rm -rf /data/sites /data/trash /data/pending /data/logs /data/previous /data/changes /data/paused /data/drafts; tar -C /data -xzf -' \
    < "$stacknest_backup_dir/files.tar.gz"
  docker compose rm -f migrate
  docker compose up -d --wait --wait-timeout 120
)
```

数据库与文件不是共同事务。失败保留备份和现场，从同批备份重做；启动后检查用户、版本、站点和暂停状态。
