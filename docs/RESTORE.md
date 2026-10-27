# 恢复参考

完整恢复优先使用 [迁移包](MIGRATION.md)导入空白目标。包中包含固定镜像、配置、数据库和文件，便于一起验证。

日常备份只有数据库与文件，需另保留匹配的平台版本、运行配置和原认证密钥。换回旧镜像不会回退数据库或应用数据。

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
  docker compose up -d --wait --wait-timeout 120
)
```

数据库与文件不是共同事务。失败保留备份和现场，从同批备份重做；启动后检查用户、版本、站点和暂停状态。

同机演练使用新目录、项目名及回环端口，避免覆盖原实例。
