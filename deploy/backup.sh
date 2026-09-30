#!/usr/bin/env bash
set -euo pipefail
umask 077

# 以同一时间点备份数据库、站点、恢复标记和操作日志；调用方指定 Compose 文件。
compose_file=$(realpath "${1:?usage: backup.sh COMPOSE_FILE BACKUP_ROOT}")
backup_root=$(realpath -m "${2:?backup root required}")
keep_count=${STACKNEST_BACKUP_KEEP:-14}
[[ "$keep_count" =~ ^[1-9][0-9]*$ ]] || { echo 'invalid retention count' >&2; exit 2; }
test -f "$compose_file"
install -d -m 700 "$backup_root"
exec 9>"$backup_root/.backup.lock"
flock -n 9 || { echo 'another backup is running' >&2; exit 3; }
cd "$(dirname "$compose_file")"
compose=(docker compose -f "$compose_file")
if [[ -n "${STACKNEST_COMPOSE_PROJECT:-}" ]]; then compose+=(-p "$STACKNEST_COMPOSE_PROJECT"); fi
"${compose[@]}" config --quiet
container_id=$("${compose[@]}" ps -q stacknest)
was_running=false
if [[ -n "$container_id" ]] && [[ $(docker inspect -f '{{.State.Running}}' "$container_id") == true ]]; then was_running=true; fi
name="stacknest-$(date -u +%Y%m%dT%H%M%SZ)-$$"
partial="$backup_root/.partial-$name"
final="$backup_root/$name"
mkdir "$partial"

# 任何备份失败都恢复原本运行的 API，并保留以 .partial 开头的现场。
finish() {
    result=$?
    trap - EXIT
    if [[ "$was_running" == true ]]; then
        if ! "${compose[@]}" start stacknest >/dev/null; then result=1; fi
    fi
    exit "$result"
}
trap finish EXIT
if [[ "$was_running" == true ]]; then "${compose[@]}" stop -t 35 stacknest >/dev/null; fi
"${compose[@]}" exec -T postgres pg_dump -U stacknest -d stacknest -Fc --no-owner --no-privileges > "$partial/postgres.dump"
"${compose[@]}" run --rm --no-deps -T --entrypoint sh stacknest -ec \
    'mkdir -p /data/sites /data/trash /data/pending /data/logs /data/previous /data/changes; tar -C /data -czf - sites trash pending logs previous changes' > "$partial/files.tar.gz"
"${compose[@]}" exec -T postgres pg_restore --list < "$partial/postgres.dump" >/dev/null
tar -tzf "$partial/files.tar.gz" >/dev/null
printf 'stacknest-backup-v1\n' > "$partial/format"
printf 'compose=%s\ncreated_at=%s\n' "$compose_file" "$(date -u +%FT%TZ)" > "$partial/metadata"
(cd "$partial" && sha256sum postgres.dump files.tar.gz > SHA256SUMS)
mv "$partial" "$final"
printf 'backup=%s\n' "$final"

# 仅轮转带本脚本格式标记的完整备份，不碰历史人工备份或失败现场。
mapfile -t backups < <(find "$backup_root" -mindepth 1 -maxdepth 1 -type d -name 'stacknest-*' | sort -r)
kept=0
for item in "${backups[@]}"; do
    [[ "$item" == "$backup_root/stacknest-"* ]] || continue
    [[ $(basename "$item") =~ ^stacknest-[0-9]{8}T[0-9]{6}Z-[0-9]+$ ]] || continue
    [[ -f "$item/format" && $(cat "$item/format") == stacknest-backup-v1 ]] || continue
    kept=$((kept + 1))
    if (( kept > keep_count )); then rm -rf -- "$item"; fi
done
