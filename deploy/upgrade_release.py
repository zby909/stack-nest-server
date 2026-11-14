#!/usr/bin/env python3
"""使用已校验的部署包升级现有 StackNest，支持无数据库迁移的版本。"""
import argparse
from contextlib import contextmanager
from copy import deepcopy
import http.client
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import signal
import socket
import ssl
import stat
import subprocess
import sys
import tarfile
import tempfile

from migrate_host import backup_lock, checked_project, check_layout, digest
from release_manifest import VERSION, validate
from resolve_compose import backup_status_dir, write_private

COMPONENTS = {"stacknest": "app", "runner": "app", "nginx": "web", "migrate": "migrate"}
DOCKER = ["docker"]
RUNTIME_TIMEOUT = 180
RUNTIME = ("runner", "stacknest", "nginx")
MIGRATION_COMMAND = ["migrate", "apply", "--dir", "file:///migrations", "--url"]


# run 只报告操作阶段，避免 Compose 或 Atlas 错误输出包含数据库连接串。
def run(command, stage, timeout=90, allowed=(0,), **kwargs):
    try:
        result = subprocess.run([str(value) for value in command], capture_output=True, timeout=timeout, **kwargs)
    except subprocess.TimeoutExpired:
        raise RuntimeError(f"{stage} timed out") from None
    if result.returncode not in allowed:
        raise RuntimeError(f"{stage} failed (exit {result.returncode})")
    return result.stdout


# run_backup 给备份脚本时间执行退出处理，避免超时直接杀死恢复流程。
def run_backup(command, env, lock, timeout=900):
    with subprocess.Popen([str(value) for value in command], stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                          env=env, pass_fds=(lock.fileno(),), start_new_session=True) as process:
        try:
            output, errors = process.communicate(timeout=timeout)
        except (subprocess.TimeoutExpired, KeyboardInterrupt) as error:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.communicate(timeout=RUNTIME_TIMEOUT)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.communicate()
            raise RuntimeError("backup interrupted; attempting to restore services") from error
        if process.returncode:
            stage = re.search(rb"(?m)^backup_failed_stage=([a-z-]+)$", errors)
            tag = f" at {stage[1].decode()}" if stage else ""
            raise RuntimeError(f"backup failed{tag} (exit {process.returncode})")
        return output


# verify_release 先校验包清单与脚本，再从发布记录取固定镜像摘要。
def verify_release(root):
    entries = {}
    for line in (root / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
        checksum, name = line.split("  ", 1)
        path = PurePosixPath(name)
        if not re.fullmatch(r"[a-f0-9]{64}", checksum) or not path.parts or path.is_absolute() or ".." in path.parts or "\\" in name or name in entries:
            raise ValueError("invalid release checksum list")
        file = root / name
        if not file.is_file() or file.resolve() != file or digest(file) != checksum:
            raise ValueError("release checksum failed")
        entries[name] = checksum
    if not {"release.json", "deploy/backup.sh", "deploy/backup_status.py"} <= entries.keys():
        raise ValueError("incomplete release package")
    manifest = json.loads((root / "release.json").read_text(encoding="utf-8"))
    if manifest.get("format") != "stacknest-release-v1" or manifest.get("platform") != "linux/amd64":
        raise ValueError("unsupported release package")
    validate(manifest)
    return manifest


# version_order 按 SemVer 比较正式版与候选版，拒绝误用旧包升级。
def version_order(version):
    if not re.fullmatch(VERSION, version):
        raise ValueError("running StackNest version is not a published version")
    base, separator, suffix = version[1:].partition("-")
    pre = tuple((0, int(part)) if part.isdigit() else (1, part) for part in suffix.split("."))
    return tuple(map(int, base.split("."))), not separator, pre


# compose 从明确的文件和项目读取配置，保留 .env 指定的入口覆盖。
def compose(path, dotenv, project=None):
    command = DOCKER + ["compose", "--project-directory", path.parent]
    if project:
        command += ["-p", project]
    return command + (["--env-file", path] if dotenv else ["--env-file", "/dev/null", "-f", path])


# sort_mounts 统一 Compose 无序的端口和挂载列表，保留命令等有序字段。
def sort_mounts(config):
    for service in config["services"].values():
        for key in ("ports", "volumes"):
            if key in service:
                service[key].sort(key=lambda item: json.dumps(item, sort_keys=True))
    return config


# read_config 解析实际配置，拒绝不受支持的旧双入口和自定义服务布局。
def read_config(command):
    config = json.loads(run(command + ["config", "--format", "json"], "resolve Compose"))
    check_layout(config)
    if set(config["services"]) != {*COMPONENTS, "postgres"}:
        raise ValueError("upgrade requires the single-Nginx Compose layout with Runner")
    return sort_mounts(config)


# updated_config 只替换平台镜像，保留原有端口、挂载和其他覆盖。
def updated_config(config, images):
    check_layout(config)
    if set(config["services"]) != {*COMPONENTS, "postgres"}:
        raise ValueError("unsupported Compose file")
    result = deepcopy(config)
    for service, component in COMPONENTS.items():
        result["services"][service]["image"] = images[component]
    return json.dumps(result, indent=2, ensure_ascii=False) + "\n"


# updated_env 定点替换发行版本变量，不重写密钥与其他配置。
def updated_env(content, manifest):
    settings = {"STACKNEST_RELEASE": manifest["version"]}
    settings.update({f"STACKNEST_{key.upper()}_IMAGE": manifest["images"][key] for key in ("app", "web", "migrate")})
    newline = "\r\n" if "\r\n" in content else "\n"
    for key, value in settings.items():
        pattern = rf"(?m)^[ \t]*(?:export[ \t]+)?{key}[ \t]*=[^\r\n]*"
        line = f"{key}={value}"
        content, count = re.subn(pattern, line, content)
        if count > 1:
            raise ValueError("duplicate release setting in environment file")
        if not count:
            content = content.rstrip("\r\n") + newline + line + newline
    return content


# inspect_running 确认输入指向当前运行实例，返回回退镜像及备份空间估算。
def inspect_running(command, config):
    bytes_needed = 0
    for service in (*RUNTIME, "postgres"):
        cid = run(command + ["ps", "-q", service], f"find running {service}").decode().split()
        if len(cid) != 1:
            raise ValueError(f"expected one running {service} container")
        value, = json.loads(run(DOCKER + ["inspect", cid[0]], f"inspect {service}"))
        configured = config["services"][service]["image"]
        local_id = run(DOCKER + ["image", "inspect", "--format", "{{.Id}}", configured], f"inspect configured {service} image").decode().strip()
        if value["Image"] != local_id or not value["State"]["Running"]:
            raise ValueError(f"Compose configuration and running {service} differ")
        for mount in value["Mounts"]:
            if (service, mount["Destination"]) in (("stacknest", "/data"), ("postgres", "/var/lib/postgresql/data")):
                size = run(["du", "-s", "-B1", "--", mount["Source"]], f"estimate {service} backup size")
                bytes_needed += int(size.split()[0])
    return bytes_needed


# migration_checksum 从固定镜像读取 Atlas 的完整目录校验文件。
def migration_checksum(image):
    cid = run(DOCKER + ["create", "--pull", "never", "--network", "none", image], "inspect migration image").decode().strip()
    try:
        raw = run(DOCKER + ["cp", cid + ":/migrations/atlas.sum", "-"], "read migration checksum")
        with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
            item, = archive.getmembers()
            if not item.isfile() or item.size > 1 << 20:
                raise ValueError("invalid migration checksum file")
            return archive.extractfile(item).read()
    finally:
        run(DOCKER + ["rm", cid], "remove temporary migration container")


# check_migrations 核对新旧迁移文件与当前数据库，确认可安全切回旧镜像。
def check_migrations(command, config, next_image):
    migrate = config["services"]["migrate"]
    args = migrate.get("command")
    if not isinstance(args, list) or len(args) != 6 or args[:5] != MIGRATION_COMMAND:
        raise ValueError("custom migration command requires a manual upgrade")
    if migration_checksum(migrate["image"]) != migration_checksum(next_image):
        raise ValueError("migration files differ; follow the manual upgrade instructions")
    if args[5].replace("$$", "$") != config["services"]["stacknest"].get("environment", {}).get("STACKNEST_DATABASE_URL", "").replace("$$", "$"):
        raise ValueError("API and migration must use the same database")
    run(DOCKER + ["run", "--rm", "--network", "none", "--pull", "never", next_image,
                  "migrate", "validate", "--dir", "file:///migrations"], "validate packaged migrations")
    status = run(command + ["run", "--rm", "--no-deps", "-T", "--pull", "never", "migrate",
                           "migrate", "status", "--dir", "file:///migrations", "--url", args[5].replace("$$", "$"),
                           "--format", "{{ .Status }} {{ len .Pending }}"], "check current database")
    if status.strip() != b"OK 0":
        raise ValueError("database has pending or incomplete migrations")


# check_idle 防止备份时打断应用部署。
def check_idle(command):
    result = run(command + ["exec", "-T", "postgres", "psql", "-U", "stacknest", "-d", "stacknest", "-Atc",
                            "SELECT count(*) FROM container_apps WHERE status IN ('pending','deploying')"], "check deployment queue")
    if result.strip() != b"0":
        raise ValueError("wait for Docker deployments to finish before upgrading")


# nginx_entries 采用容器实际发布的端口，覆盖普通 HTTP、HTTPS、Tunnel 和 SSH 入口。
def nginx_entries(command, config):
    service = config["services"]["nginx"]
    keys = {f"{item['target']}/tcp" for item in service.get("ports", []) if item.get("protocol", "tcp") == "tcp"}
    # TLS 入口启用时，80 仅负责跳转。
    if "443/tcp" in keys:
        keys.discard("80/tcp")
    names = {"80/tcp": "HTTP", "81/tcp": "Tunnel", "82/tcp": "SSH", "443/tcp": "HTTPS"}
    cid = run(command + ["ps", "-q", "nginx"], "find running Nginx").decode().strip()
    if not cid:
        raise RuntimeError("Nginx is not running")
    ports = json.loads(run(DOCKER + ["inspect", "--format", "{{json .NetworkSettings.Ports}}", cid],
                           "inspect Nginx entry ports"))
    host = service.get("environment", {}).get("STACKNEST_ADMIN_DOMAIN", "")
    entries = []
    for key in sorted(keys & names.keys()):
        bindings = ports.get(key) or []
        if not bindings:
            raise RuntimeError(f"Nginx {names[key]} port has no active binding")
        for binding in bindings:
            address = {"0.0.0.0": "127.0.0.1", "::": "::1", "": "127.0.0.1"}.get(binding["HostIp"], binding["HostIp"])
            authority = "localhost" if key == "82/tcp" else host
            if not re.fullmatch(r"[A-Za-z0-9.-]+", authority):
                raise ValueError("Nginx admin domain is missing or invalid")
            entries.append((names[key], address, int(binding["HostPort"]), authority))
    if not entries:
        raise ValueError("no supported Nginx entry port is published")
    return entries


# entry_response 连接已发布的本机端口，使用对应的 Host 和 HTTPS SNI。
def entry_response(entry, path):
    name, address, port, host = entry
    connection = http.client.HTTPConnection(address, port, timeout=5)
    headers = {"Host": host, "Accept-Encoding": "identity"}
    if name == "Tunnel":
        headers["X-Forwarded-Proto"] = "https"
    try:
        if name == "HTTPS":
            # 源站可能使用 Cloudflare Origin CA；此处只验证本机握手和路由。
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
            context.check_hostname = False
            context.verify_mode = ssl.CERT_NONE
            connection.sock = context.wrap_socket(socket.create_connection((address, port), timeout=5),
                                                  server_hostname=host)
        connection.request("GET", path, headers=headers)
        with connection.getresponse() as response:
            return response.status, response.headers.get_content_type(), response.read(128 << 10)
    finally:
        connection.close()


# check_entry 确认首页是管理界面，健康请求确实经 Nginx 到达 API 和数据库。
def check_entry(command, config):
    for entry in nginx_entries(command, config):
        name = entry[0]
        try:
            status, content_type, body = entry_response(entry, "/")
            if status != 200 or content_type != "text/html" or b"<title>StackNest</title>" not in body:
                raise RuntimeError(f"Nginx {name} did not return the StackNest homepage (HTTP {status})")
            status, content_type, body = entry_response(entry, "/api/health")
            if status != 200 or content_type != "application/json" or json.loads(body).get("status") != "ok":
                raise RuntimeError(f"Nginx {name} API health check failed (HTTP {status})")
        except (OSError, http.client.HTTPException, ValueError, AttributeError) as error:
            raise RuntimeError(f"Nginx {name} could not complete its local HTTP check") from error
    print("entry=verified", flush=True)


# check_health 用实际版本、入口和平台诊断验收；SMTP 连接故障单独提示。
def check_health(command, version):
    result = json.loads(run(command + ["exec", "-T", "stacknest", "/usr/local/bin/stacknest", "version"], "check running version"))
    if result["version"] != version:
        raise RuntimeError("running version differs from the release")
    run(command + ["exec", "-T", "nginx", "nginx", "-t"], "validate Nginx")
    check_entry(command, read_config(command))
    report = json.loads(run(command + ["exec", "-T", "stacknest", "/usr/local/bin/stacknest", "doctor", "--json"],
                            "check platform", allowed=(0, 1)))
    failed = {item["name"] for item in report["checks"] if item["status"] == "fail"}
    if failed - {"smtp"} or report["failed"] and not failed:
        raise RuntimeError("platform diagnostics failed: " + (", ".join(sorted(failed - {"smtp"})) or "unknown check"))
    if "smtp" in failed:
        print("warning=SMTP TLS or authentication failed; check login email delivery", flush=True)
    return result["revision"]


# pause_timer 在升级期间暂停已有定时备份，并恢复原先的启动状态。
@contextmanager
def pause_timer(unit):
    active = unit and subprocess.run(["systemctl", "is-active", "--quiet", unit], timeout=12).returncode == 0
    if active:
        run(["systemctl", "stop", unit], "pause backup timer")
    try:
        yield
    finally:
        if active:
            run(["systemctl", "start", unit], "resume backup timer")


# stage_config 用临时文件验证候选配置，路径解析保持在原部署目录。
@contextmanager
def stage_config(path, content):
    fd, name = tempfile.mkstemp(prefix=".stacknest-upgrade-", suffix=path.suffix, dir=path.parent)
    stage = Path(name)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as file:
            file.write(content)
        yield stage
    finally:
        stage.unlink(missing_ok=True)


# ensure_candidate 核对配置实际解析出的镜像、项目及原有运行设置。
def ensure_candidate(old, new, images):
    expected = json.loads(updated_config(old, images))
    if new != expected:
        raise ValueError("candidate changes settings beyond the release images")


# backup 使用原镜像创建一致快照，并验证数据库和文件的摘要。
def backup(root, state_dir, current, project, backup_root, status, lock, *, timeout=900, keep=None):
    snapshot = state_dir / "compose.before.json"
    write_private(snapshot, json.dumps(current, indent=2, ensure_ascii=False) + "\n")
    print("stage=backup", flush=True)
    env = dict(os.environ, STACKNEST_COMPOSE_PROJECT=project, STACKNEST_BACKUP_LOCK_FD=str(lock.fileno()))
    env.pop("STACKNEST_APPS_FROZEN", None)
    if keep is not None:
        env["STACKNEST_BACKUP_KEEP"] = str(keep)
    result = run_backup(["bash", root / "deploy/backup.sh", snapshot, backup_root, status], env, lock, timeout)
    values = [Path(line.removeprefix("backup=")) for line in result.decode().splitlines() if line.startswith("backup=")]
    if len(values) != 1 or values[0].parent != backup_root or not values[0].is_dir() or values[0].is_symlink():
        raise RuntimeError("backup did not return a completed directory")
    run(["sha256sum", "--check", "--strict", "SHA256SUMS"], "verify backup", 300, cwd=values[0])
    print("backup=" + str(values[0]), flush=True)


# start 用已经拉取的镜像启动入口、API 和 Runner，保留现有 Postgres 与命名卷。
def start(command):
    run(command + ["up", "-d", "--no-deps", "--pull", "never", "--wait", "--wait-timeout", "120", *RUNTIME],
        "start StackNest", RUNTIME_TIMEOUT)


# ensure_original_files 避免覆盖准备和备份期间由运维修改的文件。
def ensure_original_files(updates):
    for path, (original, _) in updates.items():
        if regular_file(path) != path or path.read_bytes() != original:
            raise ValueError("deployment files changed during preparation; retry with the current configuration")


# apply_release 保留配置快照，切换失败时恢复原镜像及入口。
def apply_release(root, command, project, current, updates, old_version, manifest, backup_root, status, timer):
    with backup_lock(project) as lock, pause_timer(timer):
        ensure_original_files(updates)
        if read_config(command) != current:
            raise ValueError("Compose configuration changed during preparation")
        check_idle(command)
        folder = backup_root / ".upgrades"
        folder.mkdir(mode=0o700, exist_ok=True)
        if folder.is_symlink() or (os.name == "posix" and (folder.stat().st_uid != os.geteuid() or folder.stat().st_mode & 0o077)):
            raise ValueError("upgrade state directory must be private and owned by the operator")
        state_dir = Path(tempfile.mkdtemp(prefix=manifest["version"] + "-", dir=folder))
        print("upgrade_state=" + str(state_dir), flush=True)
        for index, (path, (content, _)) in enumerate(updates.items()):
            write_private(state_dir / f"original-{index}-{path.name}", content)
        try:
            backup(root, state_dir, current, project, backup_root, status, lock)
        except (Exception, KeyboardInterrupt):
            print("stage=restore_after_backup", flush=True)
            start(command)
            run(command + ["exec", "-T", "runner", "/usr/local/bin/stacknest", "apps-thaw"], "resume existing Docker applications")
            check_health(command, old_version)
            print("original_services=verified", flush=True)
            raise
        ensure_original_files(updates)
        if read_config(command) != current:
            raise ValueError("Compose configuration changed during backup")
        permissions = {path: path.stat() for path in updates}
        try:
            print("stage=switch", flush=True)
            run(command + ["stop", "-t", "35", "nginx", "stacknest", "runner"], "stop original services")
            check_idle(command)
            for path, (_, content) in updates.items():
                replace_config(path, content, permissions[path])
            start(command)
            revision = check_health(command, manifest["version"])
            if revision != manifest["sources"]["server"]["commit"]:
                raise RuntimeError("running revision differs from the release")
        except (Exception, KeyboardInterrupt):
            print("stage=rollback", flush=True)
            for path, (content, _) in updates.items():
                replace_config(path, content, permissions[path])
            start(command)
            check_health(command, old_version)
            print("rollback=verified", flush=True)
            raise
        print("upgrade_complete=" + manifest["version"], flush=True)


# replace_config 保留实际部署文件的所有者和权限，避免 sudo 升级后普通用户无法读取。
def replace_config(path, content, original):
    owner = (original.st_uid, original.st_gid) if os.name == "posix" else None
    write_private(path, content, owner=owner, mode=stat.S_IMODE(original.st_mode))


# regular_file 拒绝符号链接，防止原子替换误写软链的目标。
def regular_file(path):
    path = Path(path).absolute()
    if path.is_symlink() or not path.is_file():
        raise ValueError("expected an existing regular deployment file")
    return path.resolve()


# upgrade 校验候选配置、磁盘和迁移后，按需进入维护窗口。
def upgrade(args):
    root = args.release.resolve(strict=True)
    manifest = verify_release(root)
    source = regular_file(args.compose or args.env_file or ".env")
    dotenv = args.compose is None
    command = compose(source, dotenv, args.project)
    current = read_config(command)
    project = checked_project(current["name"])
    command = compose(source, dotenv, project)
    bytes_needed = inspect_running(command, current)
    original = json.loads(run(command + ["exec", "-T", "stacknest", "/usr/local/bin/stacknest", "version"], "read current version"))
    old_version = original["version"]
    if version_order(manifest["version"]) <= version_order(old_version):
        raise ValueError("target release must be newer than the running version")
    images = manifest["images"]
    original_bytes = source.read_bytes()
    config_content = original_bytes.decode("utf-8")
    next_config = updated_env(config_content, manifest) if dotenv else updated_config(json.loads(config_content), images)
    updates = {source: (original_bytes, next_config)}
    public = source.with_name("compose.public.json")
    if not dotenv and source.name == "compose.json" and public.exists():
        public = regular_file(public)
        original_public = public.read_bytes()
        updates[public] = (original_public, updated_config(json.loads(original_public), images))
    with stage_config(source, next_config) as staged:
        candidate = read_config(compose(staged, dotenv, project))
    ensure_candidate(current, candidate, images)
    snapshot = args.backup_compose or (source.parent / "compose.resolved.json" if dotenv else None)
    if snapshot and (snapshot.exists() or args.backup_compose):
        snapshot = regular_file(snapshot)
        if snapshot in updates:
            raise ValueError("backup snapshot must be a separate file")
        updates[snapshot] = (snapshot.read_bytes(), json.dumps(candidate, indent=2, ensure_ascii=False) + "\n")
    status = backup_status_dir(current)
    backup_root = args.backup_root.resolve()
    ancestor = next(path for path in (backup_root, *backup_root.parents) if path.is_dir())
    docker_root = Path(run(DOCKER + ["info", "--format", "{{.DockerRootDir}}"], "read Docker storage").decode().strip())
    reserve = 512 << 20
    if shutil.disk_usage(ancestor).free < bytes_needed + reserve or shutil.disk_usage(docker_root).free < reserve:
        raise ValueError("insufficient space for backup or new images")
    print(f"preparing={old_version} -> {manifest['version']} project={project}", flush=True)
    for component, image in images.items():
        local = subprocess.run(DOCKER + ["image", "inspect", image], capture_output=True, timeout=90)
        if local.returncode:
            run(DOCKER + ["pull", image], f"pull {component}", 1200)
    if shutil.disk_usage(ancestor).free < bytes_needed + reserve:
        raise ValueError("insufficient backup space after pulling images")
    check_migrations(command, current, images["migrate"])
    check_idle(command)
    check_entry(command, current)
    if args.check:
        print("preflight=passed", flush=True)
        return
    backup_root.mkdir(mode=0o700, parents=True, exist_ok=True)
    apply_release(root, command, project, current, updates, old_version, manifest, backup_root, status, args.backup_timer)


# interrupt 让维护中的 TERM 进入回退流程。
def interrupt(_signal, _frame):
    raise KeyboardInterrupt


# main 接受已解压的可信包与当前 Compose 入口。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("release", type=Path, nargs="?", default=Path(__file__).resolve().parents[1],
                        help="包含 release.json 和 SHA256SUMS 的新版 server 目录，默认使用脚本所在发布包")
    inputs = parser.add_mutually_exclusive_group()
    inputs.add_argument("--compose", type=Path, help="正在运行的私有 Compose JSON")
    inputs.add_argument("--env-file", type=Path, help="正在使用的 .env，默认当前目录 .env")
    parser.add_argument("--project", type=checked_project)
    parser.add_argument("--backup-root", required=True, type=Path)
    parser.add_argument("--backup-compose", type=Path, help="定时备份读取的独立 Compose 快照")
    parser.add_argument("--backup-timer", help="现有 systemd 备份定时器名称")
    parser.add_argument("--check", action="store_true", help="预拉镜像并检查配置和迁移")
    args = parser.parse_args()
    if args.backup_timer and not re.fullmatch(r"[A-Za-z0-9_.@-]+\.timer", args.backup_timer):
        parser.error("invalid backup timer name")
    if sys.platform != "linux" or os.geteuid() != 0:
        parser.error("run on the Linux Docker host with sudo")
    os.umask(0o077)
    signal.signal(signal.SIGTERM, interrupt)
    try:
        # Compose 的 COMPOSE_FILE 相对路径按进程目录解释。
        args.release = args.release.resolve(strict=True)
        for name in ("compose", "env_file", "backup_root", "backup_compose"):
            path = getattr(args, name)
            if path is not None:
                setattr(args, name, path.absolute())
        os.chdir((args.compose or args.env_file or Path.cwd() / ".env").parent)
        upgrade(args)
    except (ValueError, RuntimeError) as error:
        parser.exit(1, f"upgrade_failed={error}\n")
    except (OSError, KeyError, TypeError, StopIteration, subprocess.TimeoutExpired, KeyboardInterrupt):
        parser.exit(1, "upgrade_failed=check deployment files, permissions and the reported stage\n")


if __name__ == "__main__":
    main()
