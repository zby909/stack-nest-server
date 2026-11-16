# 恢复参考

完整恢复优先使用 [迁移包](MIGRATION.md)导入空白目标。包中包含固定镜像、配置、数据库和文件，便于一起验证。

日常备份只有数据库与文件，需另保留匹配的平台版本、运行配置和原认证密钥。换回旧镜像不会回退数据库或应用数据。

## 演练日常备份

准备备份时的私有 Compose JSON（可用 [配置解析工具](OPERATIONS.md#备份)保存）及其原平台镜像。Linux 宿主执行：

```bash
sudo python3 deploy/restore_backup.py /备份目录/stacknest-时间 +  /配置目录/compose.resolved.json /opt/stacknest-restore-test +  --project stacknest-restore-test --preview-port 19080
```

工具会校验备份摘要，恢复到新数据库与数据卷，核对迁移状态和本地镜像。入口仅监听 `127.0.0.1:19080`。演练使用本机邮件设置，Docker 应用保持停止，可检查还原的应用数据目录。

通过 SSH 转发访问 `http://localhost:19080`，已发布的静态站点使用 `http://stack-nest-项目标识.localhost:19080`。结束后执行工具给出的清理命令，移除本次容器和卷，按需删除演练目录内的私有配置。

## 含 Docker 应用的实例

恢复需要同时核对应用固定镜像、加密配置、数据目录和应运行状态。使用完整迁移包在空白目标验收，再切换入口；不要将下方静态实例的原地恢复命令套用到含应用的备份。

## 仅静态实例的原地恢复

仅适用于没有 Docker 应用的目标和备份。在实际部署目录操作，先备份当前目标；任一步失败时保持 API 与入口停止。

```bash
(
  set -euo pipefail
  stacknest_backup_dir=/实际备份目录
  (cd "$stacknest_backup_dir" && sha256sum -c SHA256SUMS)
  tar -tzf "$stacknest_backup_dir/files.tar.gz" > /dev/null
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
