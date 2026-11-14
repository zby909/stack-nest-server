#!/usr/bin/env python3
"""原子保存实际 Compose 配置，保留覆盖文件且不输出凭据。"""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


# backup_status_dir 备份摘要使用 API 实际挂载的目录。
def backup_status_dir(config):
    mount = next((mount for mount in config["services"]["stacknest"].get("volumes", [])
                  if mount.get("type") == "bind" and mount.get("target") == "/run/stacknest-backup"), None)
    if not mount:
        raise ValueError("backup status mount is required")
    return Path(mount["source"])


# resolve 只有完整解析成功后才替换旧快照，避免定时备份读到空配置或半文件。
def resolve(destination, project=None, files=(), env_files=()):
    target = Path(destination).absolute()
    if target.suffix != ".json" or not target.parent.is_dir():
        raise ValueError("destination must be a .json file in an existing private directory")
    if project and not re.fullmatch(r"[a-z0-9][a-z0-9_-]*", project):
        raise ValueError("invalid Compose project name")
    command = ["docker", "compose"]
    for path in env_files:
        command += ["--env-file", str(Path(path).resolve(strict=True))]
    if project:
        command += ["-p", project]
    for path in files:
        command += ["-f", str(Path(path).resolve(strict=True))]
    command += ["config", "--format", "json"]
    result = subprocess.run(command, capture_output=True, text=True, encoding="utf-8", timeout=60)
    if result.returncode:
        raise RuntimeError("Compose configuration could not be resolved; existing file retained")
    try:
        value = json.loads(result.stdout)
    except (TypeError, ValueError):
        raise ValueError("Compose returned invalid JSON; existing file retained") from None
    if not isinstance(value, dict) or not isinstance(value.get("services"), dict) or not value["services"]:
        raise ValueError("Compose configuration lacks services; existing file retained")
    # 保留 Docker 已经转义的美元符号。
    write_private(target, result.stdout.rstrip() + "\n")
    return target


# write_private 原子替换私有配置，写入失败时保留原文件。
def write_private(target, content, *, owner=None, mode=0o600):
    descriptor, temporary = tempfile.mkstemp(prefix=".compose-resolved-", suffix=".json", dir=target.parent)
    temporary = Path(temporary)
    try:
        with os.fdopen(descriptor, "wb") as output:
            if owner is not None:
                os.chown(temporary, *owner)
            os.chmod(temporary, mode)
            output.write(content.encode("utf-8") if isinstance(content, str) else content)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, target)
        if os.name == "posix":
            directory = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
    finally:
        if temporary.exists():
            temporary.unlink()


# main 仅显示目标路径；CLI、JSON 及系统异常不回显 Compose 内容或环境。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("destination", nargs="?", default="compose.resolved.json")
    parser.add_argument("--project")
    parser.add_argument("--file", action="append", default=[])
    parser.add_argument("--env-file", action="append", default=[])
    args = parser.parse_args()
    try:
        target = resolve(args.destination, args.project, args.file, args.env_file)
    except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired) as error:
        message = str(error) if isinstance(error, (ValueError, RuntimeError)) else "could not save Compose snapshot; inspect Docker and destination permissions"
        parser.exit(1, "compose_resolve_failed=" + message + "\n")
    print("compose_resolved=" + str(target))


if __name__ == "__main__":
    main()
