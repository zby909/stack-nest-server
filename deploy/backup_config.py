"""为日常备份保存私有 Compose 快照，按内容复用，恢复时核对同一份配置。"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

from resolve_compose import write_private


# config_bytes 固定 JSON 键顺序和编码，保留 Compose 已转义的值及列表顺序。
def config_bytes(config):
    return (json.dumps(config, sort_keys=True, ensure_ascii=False, indent=2) + "\n").encode("utf-8")


# check_private 只使用当前账户拥有的私有普通文件或目录。
def check_private(path, directory=False):
    info = path.lstat()
    kind = stat.S_ISDIR if directory else stat.S_ISREG
    if not kind(info.st_mode) or (os.name == "posix" and
            (info.st_uid != os.geteuid() or info.st_mode & 0o077)):
        raise ValueError("配置快照需由备份账户私有保存，目录使用 0700，文件使用 0600")


# save_snapshot 在实例备份锁内只写一次相同配置，已损坏的快照会阻止备份。
def save_snapshot(root, config):
    check_private(root, directory=True)
    data = config_bytes(config)
    identifier = hashlib.sha256(data).hexdigest()
    vault = root / ".configs"
    vault.mkdir(mode=0o700, exist_ok=True)
    check_private(vault, directory=True)
    path = vault / (identifier + ".json")
    if path.exists() or path.is_symlink():
        check_private(path)
        if path.read_bytes() != data:
            raise ValueError("已有配置快照校验失败")
    else:
        write_private(path, data)
    return identifier


# load_snapshot 只按固定格式的摘要在私有目录取文件，复制备份根目录后也可恢复。
def load_snapshot(root, identifier):
    if not re.fullmatch(r"[0-9a-f]{64}", identifier):
        raise ValueError("备份的配置快照编号无效")
    vault = root / ".configs"
    path = vault / (identifier + ".json")
    try:
        check_private(root, directory=True)
        check_private(vault, directory=True)
        check_private(path)
        data = path.read_bytes()
    except FileNotFoundError:
        raise ValueError(f"缺少私有配置快照 .configs/{identifier}.json，请复制对应快照或显式指定原配置") from None
    if hashlib.sha256(data).hexdigest() != identifier:
        raise ValueError("配置快照校验失败")
    return json.loads(data)


# platform_version 从实际容器或本地镜像读取发布标签；源码镜像可以没有版本标签。
def platform_version(image, container):
    command = ["docker", "inspect", "--type", "container" if container else "image", "--format",
               '{{with index .Config.Labels "org.opencontainers.image.version"}}{{.}}{{end}}', container or image]
    result = subprocess.run(command, capture_output=True, timeout=15)
    label = result.stdout.decode("ascii", errors="replace").strip()
    if not result.returncode and re.fullmatch(r"[0-9A-Za-z][0-9A-Za-z.+_-]{0,95}", label):
        return label
    return "unknown"


# main 仅输出快照编号，元数据只记录编号与平台版本，不输出配置内容。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path)
    parser.add_argument("metadata", type=Path)
    parser.add_argument("project")
    parser.add_argument("container")
    args = parser.parse_args()
    try:
        config = json.load(sys.stdin.buffer)
        if config.get("name") != args.project:
            raise ValueError("实际 Compose 项目与备份目标不一致")
        version = platform_version(config["services"]["stacknest"]["image"], args.container)
        identifier = save_snapshot(args.root, config)
        write_private(args.metadata, f"config_sha256={identifier}\nplatform_version={version}\n")
    except (ValueError, OSError, KeyError, TypeError, subprocess.TimeoutExpired):
        parser.exit(1, "无法保存备份配置，请检查 Compose、镜像及私有快照目录权限和完整性\n")
    print(identifier)


if __name__ == "__main__":
    main()
