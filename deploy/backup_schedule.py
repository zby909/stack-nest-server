#!/usr/bin/env python3
"""从当前 Compose 部署启用定时备份。"""
import argparse
from contextlib import contextmanager
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile

from migrate_host import backup_lock, check_layout, checked_project
from resolve_compose import backup_status_dir, write_private
from upgrade_release import backup, check_idle, compose, interrupt, regular_file, run

DEPLOY = Path(__file__).resolve().parent
SYSTEMD = Path("/etc/systemd/system")
UNIT = "stacknest-backup"
ENTRY = Path(sys.executable)


# systemd_arg 保留路径里的空格、美元符号和百分号，拒绝控制字符。
def systemd_arg(value):
    text = str(value)
    if any(ord(char) < 32 or ord(char) == 127 for char in text):
        raise ValueError("定时任务路径不能包含控制字符")
    escaped = text.replace("\\", "\\\\").replace('"', '\\"').replace("$", "$$").replace("%", "%%")
    return '"' + escaped + '"'


# read_instance 解析实际部署，按配置定位 Compose 项目和备份摘要。
def read_instance(command):
    config = json.loads(run(command + ["config", "--format", "json"], "解析正在使用的 Compose", timeout=60))
    check_layout(config)
    return config, checked_project(config["name"]), backup_status_dir(config)


# scheduled_backup 运行前解析当前配置，并用同一份临时快照完成整次备份。
def scheduled_backup(source, dotenv, project, backup_root, keep):
    command = compose(source, dotenv, project)
    with backup_lock(project) as lock:
        config, actual, status = read_instance(command)
        running = run(command + ["ps", "--services", "--status", "running"], "检查正在运行的服务").decode().split()
        required = {"stacknest", "postgres", "runner"} & config["services"].keys()
        if actual != project or not required.issubset(running):
            raise ValueError("请确认选中了正在运行的 StackNest 实例")
        if "runner" in required:
            check_idle(command)
        check_backup_location(backup_root, status)
        if read_instance(command)[0] != config:
            raise ValueError("运行配置在备份准备期间发生变化，请重试")
        backup_root.mkdir(parents=True, exist_ok=True, mode=0o700)
        with tempfile.TemporaryDirectory(prefix="stacknest-backup-") as temporary:
            backup(DEPLOY.parent, Path(temporary), config, project, backup_root, status, lock, timeout=1500, keep=keep)


# check_backup_location 避免备份数据位于 API 摘要挂载或 Docker 数据卷内。
def check_backup_location(backup_root, status):
    status = status.resolve()
    if backup_root.is_relative_to(status) or status.is_relative_to(backup_root):
        raise ValueError("备份目录须独立于后台使用的备份摘要目录")
    docker_root = Path(run(["docker", "info", "--format", "{{.DockerRootDir}}"],
                           "检查 Docker 数据目录", timeout=15).decode().strip()).resolve()
    if backup_root.is_relative_to(docker_root):
        raise ValueError("请将备份放在 Docker 数据目录之外")
    if backup_root.exists():
        info = backup_root.stat()
        if not backup_root.is_dir() or info.st_uid != os.geteuid() or info.st_mode & 0o077:
            raise ValueError("请选择新的备份目录，或使用已有的 root 私有备份目录")


# install_lock 避免同时为两个实例安装同名定时器。
@contextmanager
def install_lock():
    import fcntl
    with open("/run/lock/stacknest-backup-install.lock", "a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError("另一项定时备份安装正在进行") from None
        yield


# enabled 检查定时任务是否已经启用并运行。
def enabled(timer):
    return all(subprocess.run(["systemctl", action, "--quiet", timer], capture_output=True, timeout=10).returncode == 0
               for action in ("is-enabled", "is-active"))


# install_backup 首次备份通过后才启用定时器；可以原样重试。
def install_backup(source, dotenv, project, backup_root, keep):
    service, timer = UNIT + ".service", UNIT + ".timer"
    command = [ENTRY, DEPLOY / "backup_schedule.py", "run",
               "--env-file" if dotenv else "--compose", source,
               "--project", project, "--backup-root", backup_root, "--keep", keep]
    exec_start = " ".join(systemd_arg(value) for value in command)
    service_text = (
        "[Unit]\nDescription=Back up StackNest database and files\nRequires=docker.service\nAfter=docker.service\n\n"
        f"[Service]\nType=oneshot\nExecStart={exec_start}\nUMask=0077\nTimeoutStartSec=35min\n"
    )
    units = {service: service_text, timer: (DEPLOY / timer).read_text(encoding="utf-8")}
    with install_lock():
        files = {}
        for name, content in units.items():
            path = SYSTEMD / name
            fragment = run(["systemctl", "show", "-p", "FragmentPath", "--value", name],
                           "检查已有的定时备份设置", timeout=15, allowed=(0, 1)).decode().strip()
            overrides = run(["systemctl", "show", "-p", "DropInPaths", "--value", name],
                            "检查定时备份覆盖设置", timeout=15, allowed=(0, 1)).decode().strip()
            if (overrides or (fragment and fragment != str(path)) or path.is_symlink()
                    or (path.exists() and path.read_bytes() != content.encode("utf-8"))):
                raise ValueError(f"{name} 已有其他配置，请继续使用原定时任务")
            files[path] = content
        if all(path.is_file() for path in files) and enabled(timer):
            print(f"定时备份已启用。备份目录：{backup_root}", flush=True)
            return
        for path, content in files.items():
            if not path.exists():
                write_private(path, content, mode=0o644)
        run(["systemctl", "daemon-reload"], "重新加载定时备份设置")
        print("正在执行首次备份，后台和运行中的应用会短暂停止。", flush=True)
        run(["systemctl", "start", service], f"首次备份失败时可用 journalctl -u {service} 查看详情", timeout=2200)
        run(["systemctl", "enable", "--now", timer], "启用每日备份")
        if not enabled(timer):
            raise RuntimeError("定时备份未启用，请查看 systemctl status " + timer)
        print(f"首次备份成功。每天服务器时间 03:15 起执行，保留最近 {keep} 份。", flush=True)
        print(f"备份目录：{backup_root}", flush=True)


# main 默认读取当前目录 .env，迁移实例可传入实际使用的 Compose JSON。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "run"))
    source = parser.add_mutually_exclusive_group()
    source.add_argument("--compose", type=Path, help="正在使用的私有 Compose 文件")
    source.add_argument("--env-file", type=Path, help="正在使用的 .env，默认当前目录 .env")
    parser.add_argument("--project", type=checked_project, help="正在运行的 Compose 项目名")
    parser.add_argument("--backup-root", type=Path, help="备份目录，默认部署目录下的 backups")
    parser.add_argument("--keep", type=int, default=14, help="保留份数，默认 14")
    args = parser.parse_args()
    if sys.platform != "linux" or os.geteuid() != 0:
        parser.error("请在 Linux Docker 宿主机上用 sudo 运行")
    if args.keep < 1:
        parser.error("备份保留份数至少为 1")
    signal.signal(signal.SIGTERM, interrupt)
    os.umask(0o077)
    try:
        path = regular_file(args.compose or args.env_file or ".env")
        root = (args.backup_root or path.parent / "backups").resolve()
        os.chdir(path.parent)
        dotenv = args.compose is None
        if args.action == "install":
            _, project, status = read_instance(compose(path, dotenv, args.project))
            check_backup_location(root, status)
            install_backup(path, dotenv, project, root, args.keep)
        else:
            project = args.project or read_instance(compose(path, dotenv))[1]
            scheduled_backup(path, dotenv, project, root, args.keep)
    except (RuntimeError, ValueError) as error:
        parser.exit(1, f"备份任务未完成：{error}\n")
    except (OSError, KeyError, TypeError, subprocess.TimeoutExpired, KeyboardInterrupt):
        parser.exit(1, "备份任务未完成，请检查部署文件、目录权限和 systemd 日志。\n")


if __name__ == "__main__":
    main()
