#!/usr/bin/env python3
"""把一份日常备份恢复到新的回环演练实例，便于验证数据库与文件。"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import shutil
import socket
import sys
import tarfile

from backup_config import config_bytes, load_snapshot, service_image
from migrate_host import (backup_lock, check_layout, checked_project, digest, ensure_empty,
                          files_size, restore_database, restore_files, run, write_json)
from upgrade_release import check_entry

ARCHIVES = {"postgres.dump", "files.tar.gz"}
AUTH_SETTINGS = (
    "STACKNEST_AUTH_SECRET", "STACKNEST_AUTH_ADMIN_EMAIL",
    "STACKNEST_AUTH_SUPER_ADMIN_EMAIL", "STACKNEST_AUTH_INITIAL_USER_EMAIL",
)


# verify_backup 只接受已完成的日常备份，并在写目标前检查摘要与归档路径。
def verify_backup(backup):
    for name in ARCHIVES | {"SHA256SUMS", "format"}:
        file = backup / name
        if file.is_symlink() or not file.is_file():
            raise ValueError("备份缺少普通文件 " + name)
    marker = (backup / "format").read_text(encoding="utf-8").strip()
    if marker not in ("stacknest-backup-v1", "stacknest-backup-v2"):
        raise ValueError("不是已完成的 StackNest 日常备份")
    required = ARCHIVES
    if marker == "stacknest-backup-v2":
        metadata = backup / "metadata"
        if metadata.is_symlink() or not metadata.is_file() or metadata.stat().st_size > 8192:
            raise ValueError("备份配置记录缺失或无效")
        required = ARCHIVES | {"metadata"}
    verified = set()
    for line in (backup / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        checksum, _, name = line.partition("  ")
        if name not in required or name in verified or not re.fullmatch(r"[a-f0-9]{64}", checksum):
            raise ValueError("备份校验清单无效")
        if digest(backup / name) != checksum:
            raise ValueError("备份校验失败 " + name)
        verified.add(name)
    if verified != required:
        raise ValueError("备份校验清单不完整")
    return files_size(backup / "files.tar.gz")


# original_config 自动读取同批快照；显式提供配置时也校验编号，兼容旧备份的三参数用法。
def original_config(backup, source):
    identifier = None
    if (backup / "format").read_text("utf-8").strip() == "stacknest-backup-v2":
        fields = dict(line.split("=", 1) for line in (backup / "metadata").read_text("utf-8").splitlines())
        identifier = fields.get("config_sha256", "")
        if not re.fullmatch(r"[0-9a-f]{64}", identifier):
            raise ValueError("备份的配置快照编号无效")
        if source is None:
            return load_snapshot(backup.parent, identifier)
    if source is None:
        raise ValueError("旧备份需要显式指定备份时的私有 Compose 配置：备份目录 配置文件 演练目录")
    source = source.resolve(strict=True)
    if identifier and digest(source) == identifier:
        return json.loads(source.read_bytes())
    config = json.loads(run(["docker", "compose", "--env-file", "/dev/null", "-f", source,
                             "config", "--format", "json"], stage="读取原 Compose 配置"))
    if identifier and hashlib.sha256(config_bytes(config)).hexdigest() != identifier:
        raise ValueError("所选配置与这份备份的配置快照编号不一致")
    return config


# rehearsal_config 使用原平台镜像与认证密钥，其余连接、卷和端口重新建立。
def rehearsal_config(source, port):
    check_layout(source)
    services = source["services"]
    environment = services["stacknest"].get("environment", {})
    auth = {key: environment[key] for key in AUTH_SETTINGS if key in environment}
    if not auth.get("STACKNEST_AUTH_SECRET") or not auth.get("STACKNEST_AUTH_ADMIN_EMAIL"):
        raise ValueError("需要备份时使用的认证密钥和管理员配置")
    password = secrets.token_urlsafe(24)
    database_url = f"postgres://stacknest:{password}@postgres:5432/stacknest?sslmode=disable"
    domains = {"STACKNEST_ADMIN_DOMAIN": "stacknest-drill.localhost", "STACKNEST_BASE_DOMAIN": "localhost"}
    config = {
        "volumes": {"postgres-data": {}, "stacknest-data": {}},
        "services": {
            "postgres": {
                "environment": {"POSTGRES_USER": "stacknest", "POSTGRES_DB": "stacknest", "POSTGRES_PASSWORD": password},
                "volumes": ["postgres-data:/var/lib/postgresql/data"],
                "healthcheck": {"test": ["CMD", "pg_isready", "-h", "127.0.0.1", "-U", "stacknest", "-d", "stacknest"],
                                "interval": "2s", "timeout": "3s", "retries": 30},
            },
            "migrate": {
                "command": ["migrate", "status", "--dir", "file:///migrations", "--url", database_url,
                            "--format", "{{ .Status }} {{ len .Pending }}"],
            },
            "stacknest": {
                "environment": {
                    **auth, **domains, "STACKNEST_ADDR": ":8080", "STACKNEST_DATA_DIR": "/data",
                    "STACKNEST_DATABASE_URL": database_url, "STACKNEST_SMTP_HOST": "127.0.0.1",
                    "STACKNEST_SMTP_PORT": "465", "STACKNEST_SMTP_USER": "drill@example.invalid",
                    "STACKNEST_SMTP_PASSWORD": "local-drill",
                },
                "volumes": ["stacknest-data:/data"],
                "healthcheck": {"test": ["CMD", "wget", "-q", "--spider", "http://127.0.0.1:8080/api/health"],
                                "interval": "2s", "timeout": "3s", "retries": 30},
            },
            "nginx": {
                "environment": {**domains, "NGINX_ENVSUBST_FILTER": "STACKNEST_ADMIN_DOMAIN|STACKNEST_BASE_DOMAIN"},
                "ports": [{"target": 82, "published": str(port), "host_ip": "127.0.0.1", "protocol": "tcp"}],
                "volumes": [
                    "./nginx/default.conf.template:/etc/nginx/templates/default.conf.template:ro",
                    "./nginx/ssh.conf.template:/etc/nginx/templates/ssh.conf.template:ro",
                    "stacknest-data:/data:ro",
                ],
            },
        },
    }
    for name, service in config["services"].items():
        service.update(image=service_image(source, name), pull_policy="never")
    return config


# report_images 检查原应用的固定镜像是否在本机，避免用可变标签猜测版本。
def report_images(command):
    images = run(command + ["exec", "-T", "postgres", "psql", "-U", "stacknest", "-d", "stacknest", "-At",
                            "-v", "ON_ERROR_STOP=1", "-c", "SELECT DISTINCT image_id FROM container_apps WHERE image_id <> ''"],
                 stage="读取应用的固定镜像")
    local = run(["docker", "image", "ls", "-a", "-q", "--no-trunc"], stage="读取本地镜像")
    pinned = set(images.split())
    missing = len(pinned - set(local.split()))
    print(f"Docker 应用固定镜像 {len(pinned)} 个，本机缺少 {missing} 个。", flush=True)


# restore 在全新项目恢复数据库和文件，启动回环入口做验收。
def restore(args):
    backup = args.backup.resolve(strict=True)
    target = args.destination.absolute()
    if target.exists() or target.is_symlink():
        raise ValueError("演练目录必须是尚不存在的路径")
    expanded = verify_backup(backup)
    config = original_config(backup, args.compose)
    if config.get("name") == args.project:
        raise ValueError("请选择与原实例不同的 Compose 项目名")
    config = rehearsal_config(config, args.preview_port)
    with backup_lock(args.project):
        ensure_empty(args.project, config)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", args.preview_port))
        docker_root = Path(run(["docker", "info", "--format", "{{.DockerRootDir}}"], stage="读取 Docker 存储目录").strip())
        needed = expanded * 2 + (backup / "postgres.dump").stat().st_size * 4 + (512 << 20)
        if shutil.disk_usage(docker_root).free < needed:
            raise ValueError("Docker 存储剩余空间不足")
        for name, service in config["services"].items():
            run(["docker", "image", "inspect", "--format", "{{.Id}}", service["image"]],
                stage=f"检查 {name} 镜像，请先准备原版本镜像")
        target.mkdir(mode=0o700)
        (target / "nginx").mkdir(mode=0o700)
        for template in ("default.conf.template", "ssh.conf.template"):
            shutil.copyfile(Path(__file__).resolve().parent / "nginx" / template, target / "nginx" / template)
        path = target / "compose.json"
        write_json(path, config)
        command = ["docker", "compose", "--env-file", "/dev/null", "-p", args.project, "-f", path]
        print("演练项目 " + args.project, flush=True)
        print("清理演练实例 " + shlex.join(["sudo", *map(str, command), "down", "--volumes"]), flush=True)
        run(command + ["config", "--quiet"], stage="校验演练配置")
        try:
            print("恢复数据库…", flush=True)
            restore_database(command, backup / "postgres.dump")
            result = run(command + ["run", "--rm", "--no-deps", "-T", "migrate"], stage="检查恢复后数据库的迁移状态")
            if result.strip() != "OK 0":
                raise ValueError("数据库迁移状态与镜像不匹配，请使用备份时的平台版本")
            print("恢复站点与 Docker 数据目录…", flush=True)
            restore_files(command, backup / "files.tar.gz")
            report_images(command)
            run(command + ["up", "-d", "--wait", "--wait-timeout", "120", "stacknest", "nginx"], stage="启动演练服务")
            check_entry(command, config)
        except (Exception, KeyboardInterrupt):
            try:
                run(command + ["stop"], stage="停止失败的演练服务")
            except RuntimeError as error:
                print(error, file=sys.stderr)
            raise
        print(f"恢复演练就绪 http://127.0.0.1:{args.preview_port}/", flush=True)
        print("可验证静态站点和数据库。演练期间应用数据留在副本，邮件只会尝试连接本机。", flush=True)


# main 只接受 Linux 宿主路径并限制回环预览端口。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("backup", type=Path, help="已完成的日常备份目录")
    parser.add_argument("compose", nargs="?", type=Path, help="备份时的私有 Compose 配置，旧备份必填")
    parser.add_argument("destination", type=Path, help="新的演练目录")
    parser.add_argument("--project", type=checked_project, required=True, help="新的 Compose 项目名")
    parser.add_argument("--preview-port", type=int, default=19080, help="回环预览端口")
    args = parser.parse_args()
    if sys.platform != "linux" or os.geteuid() != 0:
        parser.error("请在 Linux Docker 宿主机上使用 sudo")
    if not 1024 <= args.preview_port <= 65535:
        parser.error("预览端口需要在 1024 至 65535 之间")
    os.umask(0o077)
    try:
        restore(args)
    except (ValueError, RuntimeError) as error:
        parser.exit(1, f"恢复演练失败 {error}\n")
    except (OSError, KeyError, TypeError, tarfile.TarError, KeyboardInterrupt):
        parser.exit(1, "恢复演练失败，请检查备份文件、权限和已报告的阶段\n")


if __name__ == "__main__":
    main()
