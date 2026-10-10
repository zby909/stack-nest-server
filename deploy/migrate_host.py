#!/usr/bin/env python3
"""将当前 Compose 实例迁移至空白 Linux Docker 环境；不覆盖已有实例。"""
import argparse
import copy
from contextlib import contextmanager
import hashlib
import json
import os
import posixpath
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile
from datetime import datetime, timezone

FORMAT = "stacknest-migration-v1"
SERVICES = {"postgres", "migrate", "stacknest", "nginx", "tunnel"}
DATA_DIRS = {"sites", "previous", "changes", "trash", "pending", "logs", "paused", "drafts", "apps"}

# run 隐藏命令输出中的连接串和配置；错误仅显示阶段与返回码。
def run(args, *, stdin=None, stdout=None, env=None, stage="docker", pass_fds=()):
    result = subprocess.run([str(arg) for arg in args], stdin=stdin, stdout=stdout or subprocess.PIPE, stderr=subprocess.PIPE, env=env, pass_fds=pass_fds)
    if result.returncode:
        raise RuntimeError(f"{stage} failed (exit {result.returncode}); inspect the target container logs locally")
    return result.stdout.decode("utf-8") if result.stdout else ""

# digest 以固定内存校验大镜像和数据文件。
def digest(path):
    checksum = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            checksum.update(chunk)
    return checksum.hexdigest()

# image_store 区分使用不同固定镜像编号的两类 Docker 镜像存储。
def image_store(info):
    entries = info.get("DriverStatus") or []
    return "containerd" if any(len(item) == 2 and item[0] == "driver-type" and str(item[1]).startswith("io.containerd.snapshotter.") for item in entries) else "classic"

# check_image_store 在任何目标写入前拒绝需要额外镜像摘要转换的跨后端导入。
def check_image_store(manifest, info):
    expected = manifest.get("imageStore")
    if expected is not None and expected != image_store(info):
        raise ValueError("source and destination Docker image stores differ; configure the same image storage backend before importing")

# write_json 限制包含密钥的文件权限，不把配置内容写到终端。
def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    path.chmod(0o600)

# compose 避免继承旧项目名，所有操作显式指向目标文件和项目。
def compose(path, project):
    return ["docker", "compose", "-p", project, "-f", path]

# checked_project 防止含糊的项目名和选项注入。
def checked_project(value):
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]{1,50}", value):
        raise ValueError("invalid Compose project name")
    return value

# files_size 仅允许应用数据内的相对链接，禁止穿越和通过链接祖先写入其他目录。
def files_size(path):
    total, entries = 0, {}
    with tarfile.open(path, "r:gz") as archive:
        for item in archive:
            name = PurePosixPath(item.name)
            if name.is_absolute() or ".." in name.parts or not name.parts or name.parts[0] not in DATA_DIRS or "\\" in item.name:
                raise ValueError("unsafe path in data archive")
            normalized = name.as_posix()
            if normalized in entries:
                raise ValueError("duplicate path in data archive")
            entries[normalized] = item
            if not item.isdir() and not item.isfile() and not item.issym() and not item.islnk():
                raise ValueError("special files are not allowed in data archive")
            if item.issym() or item.islnk():
                if len(name.parts) < 4 or name.parts[0] != "apps" or name.parts[2] != "data" or not re.fullmatch(r"[a-f0-9-]{36}", name.parts[1]):
                    raise ValueError("links must belong to an application data directory")
                prefix = "/".join(name.parts[:3]) + "/"
                link = item.linkname
                if link.startswith("/") or "\\" in link:
                    raise ValueError("absolute links are not portable")
                resolved = posixpath.normpath(posixpath.join(str(name.parent), link) if item.issym() else link)
                if not resolved.startswith(prefix):
                    raise ValueError("application link escapes its data directory")
                if item.islnk() and (resolved not in entries or not entries[resolved].isfile()):
                    raise ValueError("hard links must reference an earlier regular file")
            total += item.size
    for name in entries:
        for parent in PurePosixPath(name).parents:
            entry = entries.get(str(parent))
            if entry and not entry.isdir():
                raise ValueError("archive entry has a non-directory ancestor")
    return total

# check_layout 兼容旧静态实例与包含独立运行器的新实例，拒绝未知服务和卷。
def check_layout(config):
    services = set(config["services"])
    volumes = {"postgres-data", "stacknest-data"}
    if "runner" in services:
        volumes.add("runner-control")
    if services not in (SERVICES, SERVICES | {"runner"}) or set(config.get("volumes", {})) != volumes:
        raise ValueError("only the StackNest release Compose layout is supported")

# verify_bundle 先检查完整文件清单与摘要，未完成包不允许导入。
def verify_bundle(bundle):
    manifest_file = bundle / "manifest.json"
    if manifest_file.is_symlink() or manifest_file.stat().st_size > 1 << 20:
        raise ValueError("invalid migration manifest")
    manifest = json.loads(manifest_file.read_text(encoding="utf-8"))
    if manifest.get("format") != FORMAT:
        raise ValueError("unsupported migration format")
    if manifest.get("imageStore") not in (None, "classic", "containerd"):
        raise ValueError("unsupported Docker image store in manifest")
    expected = manifest["files"]
    required = {"images.tar", "compose.json", "data/postgres.dump", "data/files.tar.gz", "deploy/backup.sh", "deploy/backup_status.py", "deploy/migrate_host.py"}
    if not required.issubset(expected):
        raise ValueError("incomplete migration bundle")
    actual = set()
    for path in bundle.rglob("*"):
        if path.is_symlink():
            raise ValueError("migration bundle cannot contain links")
        if path.is_file() and path != manifest_file:
            actual.add(path.relative_to(bundle).as_posix())
    if actual != set(expected):
        raise ValueError("migration file list differs from manifest")
    for name, checksum in expected.items():
        relative = PurePosixPath(name)
        if relative.is_absolute() or ".." in relative.parts or "\\" in name or digest(bundle / name) != checksum:
            raise ValueError("migration checksum or path validation failed")
    files_size(bundle / "data/files.tar.gz")
    return manifest

# portable_config 只迁移本项目的数据卷和明确的入口配置，不打包任意主机目录。
def portable_config(config, destination):
    check_layout(config)
    result = copy.deepcopy(config)
    result.pop("name", None)
    result["volumes"] = {name: {} for name in config["volumes"]}
    for network in result.get("networks", {}).values():
        if network.get("external"):
            raise ValueError("external Docker networks are not portable")
        network.pop("name", None)
    for service_name, service in result["services"].items():
        for key in ("build", "container_name", "env_file"):
            service.pop(key, None)
        service["pull_policy"] = "never"
        for mount in service.get("volumes", []):
            if mount["type"] == "volume":
                if mount["source"] not in result["volumes"]:
                    raise ValueError("unexpected named volume")
                continue
            if mount["type"] != "bind":
                raise ValueError("unsupported mount type")
            target = mount["target"]
            if service_name == "runner" and target == "/var/run/docker.sock" and mount["source"] == target:
                continue
            if service_name == "runner" and target == "/docker-storage" and mount.get("read_only") and PurePosixPath(mount["source"]).is_absolute():
                # 新服务器导入时替换为其真实 Docker 存储根目录。
                mount["source"] = "/var/lib/docker"
                continue
            if target == "/run/stacknest-backup":
                mount["source"] = "./backup-status"
                continue
            source = Path(mount["source"])
            if target == "/etc/nginx/certs":
                local = "cert"
                (destination / local).mkdir(mode=0o700)
                for filename in ("certificate.pem", "private.key"):
                    shutil.copyfile(source / filename, destination / local / filename)
                    (destination / local / filename).chmod(0o600)
            elif service_name == "tunnel" and target == "/etc/nginx/templates/cloudflare-tunnel.conf.template" and mount.get("read_only"):
                # 公网 Tunnel 模板与原 SSH 入口使用不同文件名，避免导出时相互覆盖。
                local = "deploy/nginx/tunnel-cloudflare.conf.template"
                (destination / local).parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                shutil.copyfile(source, destination / local)
            elif target in ("/etc/nginx/templates/default.conf.template", "/etc/nginx/conf.d/default.conf"):
                local = f"deploy/nginx/{service_name}.conf"
                (destination / local).parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                shutil.copyfile(source, destination / local)
            else:
                raise ValueError("unexpected host mount; export refused")
            mount["source"] = "./" + local
    return result


# backup_lock 与定时备份共享实例锁，避免互相重启 API 破坏一致快照。
@contextmanager
def backup_lock(project):
    import fcntl
    checked_project(project)
    with open(f"/run/lock/stacknest-backup-{project}.lock", "a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError("another backup or migration is running for this instance") from None
        yield lock

# export_bundle 生成数据快照和固定镜像包，失败时恢复原 API 的运行状态。
def export_bundle(args):
    source = Path(args.compose).resolve(strict=True)
    target = Path(args.destination).absolute()
    target.mkdir(mode=0o700, parents=False, exist_ok=False)
    command = compose(source, args.project)
    config = json.loads(run(command + ["config", "--format", "json"], stage="read compose"))
    portable = portable_config(config, target)
    source_info = json.loads(run(["docker", "info", "--format", "{{json .}}"]))
    image_ids = set()
    platforms = set()
    for name, service in config["services"].items():
        ids = run(command + ["ps", "-a", "-q", name]).split()
        if len(ids) > 1:
            raise ValueError("multiple service replicas are not supported")
        image = run(["docker", "inspect", "--format", "{{.Image}}", ids[0]]).strip() if ids else service["image"]
        info = json.loads(run(["docker", "image", "inspect", image]))[0]
        image_ids.add(info["Id"])
        platforms.add(info["Os"] + "/" + info["Architecture"])
        portable["services"][name]["image"] = info["Id"]
    if len(platforms) != 1:
        raise ValueError("service images use different platforms")
    scripts = Path(__file__).resolve().parent
    for filename in ("backup.sh", "backup_status.py", "migrate_host.py"):
        dest = target / "deploy" / filename
        dest.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        shutil.copyfile(scripts / filename, dest)
    with backup_lock(args.project) as lock:
        api_was_running = bool(run(command + ["ps", "--status", "running", "-q", "stacknest"]).strip())
        success, frozen = False, False
        try:
            if api_was_running:
                run(command + ["stop", "-t", "35", "stacknest"], stage="freeze writes")
            if "runner" in config["services"]:
                frozen = True
                run(command + ["exec", "-T", "runner", "/usr/local/bin/stacknest", "apps-freeze"], stage="freeze applications")
                # 数据库记录固定镜像 ID；恢复时重建容器，不依赖旧机器的容器 ID 或仓库可用性。
                images = run(command + ["exec", "-T", "postgres", "psql", "-U", "stacknest", "-d", "stacknest", "-Atc", "SELECT DISTINCT image_id FROM container_apps WHERE image_id <> '';"])
                for image in images.split():
                    if not re.fullmatch(r"sha256:[a-f0-9]{64}", image):
                        raise ValueError("invalid application image ID")
                    info = json.loads(run(["docker", "image", "inspect", image]))[0]
                    if info["Os"] + "/" + info["Architecture"] not in platforms:
                        raise ValueError("application image platform differs")
                    image_ids.add(info["Id"])
            print("stage=images", flush=True)
            run(["docker", "image", "save", "--output", target / "images.tar", *sorted(image_ids)], stage="save images")
            print("stage=data", flush=True)
            environment = os.environ.copy()
            environment["STACKNEST_COMPOSE_PROJECT"] = args.project
            environment["STACKNEST_BACKUP_LOCK_FD"] = str(lock.fileno())
            if frozen: environment["STACKNEST_APPS_FROZEN"] = "1"
            run(["bash", scripts / "backup.sh", source, target / ".snapshots", target / ".status"], env=environment, stage="consistent backup", pass_fds=(lock.fileno(),))
            backups = list((target / ".snapshots").glob("stacknest-*"))
            if len(backups) != 1:
                raise ValueError("expected one complete snapshot")
            (target / "data").mkdir(mode=0o700)
            for filename in ("postgres.dump", "files.tar.gz"):
                shutil.move(backups[0] / filename, target / "data" / filename)
            shutil.rmtree(target / ".snapshots")
            shutil.rmtree(target / ".status")
            # Compose config 输出的 JSON 已转义美元符号，直接复用，不能重复转义。
            write_json(target / "compose.json", portable)
            manifest = {"format": FORMAT, "createdAt": datetime.now(timezone.utc).isoformat(), "platform": platforms.pop(), "imageStore": image_store(source_info), "files": {}}
            for path in sorted(target.rglob("*")):
                if path.is_file():
                    path.chmod(0o600)
                    manifest["files"][path.relative_to(target).as_posix()] = digest(path)
            write_json(target / "manifest.json", manifest)
            verify_bundle(target)
            success = True
            print(f"export_complete={target} writes_frozen={args.freeze}")
        finally:
            if not (args.freeze and success):
                try:
                    if frozen: run(command + ["exec", "-T", "runner", "/usr/local/bin/stacknest", "apps-thaw"], stage="restore applications")
                finally:
                    if api_was_running: run(command + ["up", "-d", "--no-deps", "--no-recreate", "--wait", "--wait-timeout", "90", "stacknest"], stage="restart after export")

# ensure_empty 拒绝任何同名容器或数据卷，失败也不会清空已有数据。
def ensure_empty(project):
    checked_project(project)
    if run(["docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=" + project]).strip():
        raise ValueError("target Compose project already has containers")
    volumes = set(run(["docker", "volume", "ls", "-q"]).split())
    if {project + "_postgres-data", project + "_stacknest-data"} & volumes:
        raise ValueError("target data volumes already exist")
    if run(["docker", "volume", "ls", "-q", "--filter", "label=com.docker.compose.project=" + project]).strip():
        raise ValueError("target Compose project already has volumes")

# import_bundle 在全新实例恢复，先仅开放本机预览，确认后再 activate。
def import_bundle(args):
    bundle = Path(args.bundle).resolve(strict=True)
    manifest = verify_bundle(bundle)
    target = Path(args.destination).absolute()
    if target.exists() or target.is_symlink():
        raise ValueError("target directory must not exist")
    ensure_empty(args.project)
    platform = json.loads(run(["docker", "version", "--format", "{{json .Server}}"] ))
    if platform["Os"] + "/" + platform["Arch"] != manifest["platform"]:
        raise ValueError("source and destination Docker platforms differ")
    needed = (bundle / "images.tar").stat().st_size * 3 + files_size(bundle / "data/files.tar.gz") * 2 + (bundle / "data/postgres.dump").stat().st_size * 4 + (512 << 20)
    destination_info = json.loads(run(["docker", "info", "--format", "{{json .}}"]))
    check_image_store(manifest, destination_info)
    docker_root = Path(destination_info["DockerRootDir"])
    if shutil.disk_usage(target.parent).free < needed or shutil.disk_usage(docker_root).free < needed:
        raise ValueError("insufficient free space for migration")
    target.mkdir(mode=0o700)
    for folder in ("cert", "deploy"):
        if (bundle / folder).exists():
            shutil.copytree(bundle / folder, target / folder)
    (target / "backup-status").mkdir(mode=0o755)
    (target / "backup-status").chmod(0o755)
    config = json.loads((bundle / "compose.json").read_text(encoding="utf-8"))
    check_layout(config)
    for mount in config["services"].get("runner", {}).get("volumes", []):
        if mount["type"] == "bind" and mount["target"] == "/docker-storage":
            mount["source"] = str(docker_root)
    config["services"]["tunnel"]["ports"] = [{"target": 80, "published": str(args.preview_port), "host_ip": "127.0.0.1", "protocol": "tcp"}]
    write_json(target / "compose.public.json", config)
    config["services"]["nginx"].pop("ports", None)
    config_path = target / "compose.json"
    write_json(config_path, config)
    command = compose(config_path, args.project)
    run(command + ["config", "--quiet"], stage="validate target config")
    print("stage=load-images", flush=True)
    run(["docker", "image", "load", "--input", bundle / "images.tar"], stage="load images")
    print("stage=restore", flush=True)
    run(command + ["up", "-d", "--wait", "--wait-timeout", "120", "postgres"], stage="start fresh database")
    try:
        tables = run(command + ["exec", "-T", "postgres", "psql", "-U", "stacknest", "-d", "stacknest", "-Atc", "SELECT count(*) FROM pg_tables WHERE schemaname='public'"])
        if tables.strip() != "0":
            raise ValueError("target database is not empty")
        with (bundle / "data/postgres.dump").open("rb") as source:
            run(command + ["exec", "-T", "postgres", "pg_restore", "-U", "stacknest", "-d", "stacknest", "--exit-on-error", "--single-transaction", "--no-owner", "--no-privileges"], stdin=source, stage="restore database")
        with (bundle / "data/files.tar.gz").open("rb") as source:
            run(command + ["run", "--rm", "--no-deps", "-T", "--user", "0:0", "--entrypoint", "sh", "stacknest", "-ec", "test -z \"$(find /data -type f -print -quit)\"; tar -xzf - -C /data"], stdin=source, stage="restore files")
        run(command + ["up", "-d", "--wait", "--wait-timeout", "120"], stage="start restored services")
        for name in ("nginx", "tunnel"):
            run(command + ["exec", "-T", name, "nginx", "-t"], stage="validate nginx")
        write_json(target / "migration-ready.json", {"format": FORMAT, "project": args.project, "previewPort": args.preview_port})
        print(f"import_complete={target} preview=http://127.0.0.1:{args.preview_port}/ public_ports_closed=true")
    except Exception:
        if "runner" in config["services"]:
            try:
                run(command + ["exec", "-T", "runner", "/usr/local/bin/stacknest", "apps-freeze"], stage="keep failed applications offline")
            except RuntimeError:
                pass
        run(command + ["stop", "stacknest", "nginx", "tunnel"], stage="keep failed import offline")
        raise

# activate 明确确认新实例后才恢复公网监听，不自动修改 DNS 或旧服务器。
def activate(args):
    target = Path(args.destination).resolve(strict=True)
    marker = json.loads((target / "migration-ready.json").read_text(encoding="utf-8"))
    if marker.get("format") != FORMAT or marker.get("project") != args.project:
        raise ValueError("target was not verified for this project")
    config_path = target / "compose.json"
    old = config_path.read_bytes()
    try:
        shutil.copyfile(target / "compose.public.json", config_path)
        run(compose(config_path, args.project) + ["up", "-d", "--wait", "--wait-timeout", "120"], stage="activate public entry")
    except Exception:
        config_path.write_bytes(old)
        run(compose(config_path, args.project) + ["up", "-d", "--wait", "--wait-timeout", "120"], stage="restore local preview")
        raise
    print("public_entry_active=true; update DNS after validating the new server")

# main 所有变更都要求显式子命令，校验命令无需 Docker 或服务器访问。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    export = sub.add_parser("export")
    export.add_argument("compose"); export.add_argument("destination")
    export.add_argument("--project", required=True, type=checked_project)
    export.add_argument("--freeze", action="store_true", help="成功后保持旧 API 停止，供最终切换使用")
    check = sub.add_parser("check"); check.add_argument("bundle")
    restore = sub.add_parser("import")
    restore.add_argument("bundle"); restore.add_argument("destination")
    restore.add_argument("--project", required=True, type=checked_project)
    restore.add_argument("--preview-port", type=int, default=18080)
    enable = sub.add_parser("activate")
    enable.add_argument("destination"); enable.add_argument("--project", required=True, type=checked_project)
    args = parser.parse_args()
    if args.action == "check":
        verify_bundle(Path(args.bundle).resolve(strict=True)); print("migration_bundle_verified=true"); return
    if sys.platform != "linux":
        raise ValueError("export/import/activate require Linux with Docker Compose")
    os.umask(0o077)
    if args.action == "export": export_bundle(args)
    elif args.action == "import":
        if not 1024 <= args.preview_port <= 65535: raise ValueError("invalid preview port")
        import_bundle(args)
    else: activate(args)

if __name__ == "__main__":
    try: main()
    except (OSError, ValueError, KeyError, RuntimeError, tarfile.TarError) as error:
        print(f"migration_failed={error}", file=sys.stderr)
        sys.exit(1)
