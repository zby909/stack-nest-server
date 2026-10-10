#!/usr/bin/env bash
set -euo pipefail
umask 077

# 以同一时间点备份数据库、站点、恢复标记和操作日志；调用方指定 Compose 文件。
compose_file=$(realpath "${1:?usage: backup.sh COMPOSE_FILE BACKUP_ROOT [STATUS_DIR]}")
backup_root=$(realpath -m "${2:?backup root required}")
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
status_dir=$(realpath -m "${3:-${STACKNEST_BACKUP_STATUS_DIR:-$(dirname "$compose_file")/backup-status}}")
keep_count=${STACKNEST_BACKUP_KEEP:-14}
[[ "$keep_count" =~ ^[1-9][0-9]*$ ]] || { echo 'invalid retention count' >&2; exit 2; }
test -f "$compose_file"
install -d -m 700 "$backup_root"
exec 9>"$backup_root/.backup.lock"
flock -n 9 || { echo 'another backup is running' >&2; exit 3; }
started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
stage=initialize
was_running=false
apps_frozen=false
compose=()

# 独立的摘要目录仅供 API 只读挂载；备份根目录仍为 0700。
write_status() {
    python3 "$script_dir/backup_status.py" "$status_dir" "$backup_root" "$1" "$started_at" "$stage"
}

# 失败时保留最近成功备份与失败阶段，并恢复原本运行的 API。
finish() {
    result=$?
    trap - EXIT INT TERM
    if [[ "$apps_frozen" == true ]]; then
        if ! "${compose[@]}" exec -T runner /usr/local/bin/stacknest apps-thaw >/dev/null; then stage=apps-restore; result=1; fi
    fi
    if [[ "$was_running" == true ]]; then
        if ! "${compose[@]}" up -d --no-deps --no-recreate --wait --wait-timeout 90 stacknest >/dev/null; then stage=restart; result=1; fi
    fi
    if (( result == 0 )); then
        stage=completed
        write_status succeeded || result=1
    else
        write_status failed || true
        printf 'backup_failed_stage=%s\n' "$stage" >&2
    fi
    exit "$result"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
write_status running
cd "$(dirname "$compose_file")"
compose=(docker compose -f "$compose_file")
if [[ -n "${STACKNEST_COMPOSE_PROJECT:-}" ]]; then compose+=(-p "$STACKNEST_COMPOSE_PROJECT"); fi
"${compose[@]}" config --quiet
instance_project=${STACKNEST_COMPOSE_PROJECT:-}
if [[ -z "$instance_project" ]]; then
    instance_project=$("${compose[@]}" config --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["name"])')
fi
[[ "$instance_project" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || { echo 'invalid instance project' >&2; exit 2; }
instance_lock="/run/lock/stacknest-backup-$instance_project.lock"
# 导出程序可传入已持有的同一文件描述符；其他调用自行获取实例锁。
if [[ -n "${STACKNEST_BACKUP_LOCK_FD:-}" ]]; then
    [[ "$STACKNEST_BACKUP_LOCK_FD" =~ ^[0-9]+$ && "$STACKNEST_BACKUP_LOCK_FD" != 8 && "$STACKNEST_BACKUP_LOCK_FD" != 9 ]] || exit 2
    [[ $(readlink "/proc/$$/fd/$STACKNEST_BACKUP_LOCK_FD") == "$instance_lock" ]] || { echo 'invalid inherited backup lock' >&2; exit 2; }
    flock -n "$STACKNEST_BACKUP_LOCK_FD" || exit 3
else
    exec 8>"$instance_lock"
    flock -n 8 || { echo 'another backup or migration is running for this instance' >&2; exit 3; }
fi
container_id=$("${compose[@]}" ps -q stacknest)
if [[ -n "$container_id" ]] && [[ $(docker inspect -f '{{.State.Running}}' "$container_id") == true ]]; then was_running=true; fi
name="stacknest-$(date -u +%Y%m%dT%H%M%SZ)-$$"
partial="$backup_root/.partial-$name"
final="$backup_root/$name"
mkdir "$partial"

stage=stop
if [[ "$was_running" == true ]]; then "${compose[@]}" stop -t 35 stacknest >/dev/null; fi
# 外层迁移程序冻结后传入标记，避免嵌套备份提前恢复应用。
if [[ "${STACKNEST_APPS_FROZEN:-}" != 1 ]] && "${compose[@]}" config --services | grep -qx runner; then
    stage=apps-freeze
    apps_frozen=true
    "${compose[@]}" exec -T runner /usr/local/bin/stacknest apps-freeze >/dev/null
fi
stage=database
"${compose[@]}" exec -T postgres pg_dump -U stacknest -d stacknest -Fc --no-owner --no-privileges > "$partial/postgres.dump"
stage=files
"${compose[@]}" run --rm --no-deps -T --user 0:0 --entrypoint sh stacknest -ec \
    'for dir in sites trash pending logs previous changes paused drafts; do if [ ! -e /data/$dir ]; then mkdir /data/$dir; chown 10001:10001 /data/$dir; fi; done; mkdir -p /data/apps; tar -C /data -czf - sites trash pending logs previous changes paused drafts apps' > "$partial/files.tar.gz"
stage=verify
"${compose[@]}" exec -T postgres pg_restore --list < "$partial/postgres.dump" >/dev/null
tar -tzf "$partial/files.tar.gz" >/dev/null
printf 'stacknest-backup-v1\n' > "$partial/format"
printf 'compose=%s\ncreated_at=%s\n' "$compose_file" "$(date -u +%FT%TZ)" > "$partial/metadata"
(cd "$partial" && sha256sum postgres.dump files.tar.gz > SHA256SUMS)
stage=publish
mv "$partial" "$final"
printf 'backup=%s\n' "$final"

# 仅轮转带本脚本格式标记的完整备份，不碰历史人工备份或失败现场。
stage=prune
mapfile -t backups < <(find "$backup_root" -mindepth 1 -maxdepth 1 -type d -name 'stacknest-*' | sort -r)
kept=0
for item in "${backups[@]}"; do
    [[ "$item" == "$backup_root/stacknest-"* ]] || continue
    [[ $(basename "$item") =~ ^stacknest-[0-9]{8}T[0-9]{6}Z-[0-9]+$ ]] || continue
    [[ -f "$item/format" && $(cat "$item/format") == stacknest-backup-v1 ]] || continue
    kept=$((kept + 1))
    if (( kept > keep_count )); then rm -rf -- "$item"; fi
done
