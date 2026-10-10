#!/usr/bin/env python3
"""发布可只读挂载给 API 的备份摘要，不读取数据库或站点文件内容。"""

import argparse
import datetime as dt
import json
import os
from pathlib import Path
import re
import tempfile


def inventory(root: Path):
    """仅统计完成标记齐全且非链接的备份，忽略中途留下的目录。"""
    complete = []
    for folder in root.iterdir():
        if not re.fullmatch(r"stacknest-\d{8}T\d{6}Z-\d+", folder.name):
            continue
        if folder.is_symlink() or not folder.is_dir():
            continue
        required = [folder / name for name in (
            "format", "metadata", "SHA256SUMS", "postgres.dump", "files.tar.gz"
        )]
        if any(path.is_symlink() or not path.is_file() for path in required):
            continue
        if required[0].stat().st_size > 128 or required[1].stat().st_size > 8192:
            continue
        try:
            if required[0].read_text().strip() != "stacknest-backup-v1":
                continue
            metadata = dict(line.split("=", 1) for line in required[1].read_text().splitlines() if "=" in line)
            created = dt.datetime.fromisoformat(metadata["created_at"].replace("Z", "+00:00"))
            if created.tzinfo is None:
                continue
            created = created.astimezone(dt.timezone.utc)
        except (KeyError, ValueError, OSError):
            continue
        database_bytes, files_bytes = required[3].stat().st_size, required[4].stat().st_size
        if database_bytes <= 0 or files_bytes <= 0:
            continue
        complete.append((folder.stat().st_mtime_ns, {
            "createdAt": created.isoformat().replace("+00:00", "Z"),
            "databaseBytes": database_bytes,
            "filesBytes": files_bytes,
            "totalBytes": database_bytes + files_bytes,
        }))
    complete.sort(key=lambda item: (item[1]["createdAt"], item[0]), reverse=True)
    return [record for _, record in complete]


def publish(args):
    """以临时文件加原子替换更新摘要，确保 API 不会读到半份 JSON。"""
    root, target = Path(args.backup_root), Path(args.status_dir)
    if not root.is_dir() or root.is_symlink() or target.is_symlink():
        raise ValueError("backup/status directory must be a regular directory")
    target.mkdir(mode=0o755, parents=True, exist_ok=True)
    # 摘要无秘密，允许非 root API 读取；备份根目录仍保持 0700。
    target.chmod(0o755)
    complete = inventory(root)
    attempt = {"startedAt": args.started_at, "stage": args.stage}
    if args.state != "running":
        attempt["finishedAt"] = dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")
    status = {
        "version": 1,
        "state": args.state,
        "lastAttempt": attempt,
        "retainedCount": len(complete),
        "retainedBytes": sum(record["totalBytes"] for record in complete),
    }
    if complete:
        status["lastSuccess"] = complete[0]
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=target, delete=False) as output:
            temporary = Path(output.name)
            json.dump(status, output, ensure_ascii=False)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
            os.fchmod(output.fileno(), 0o644)
        os.replace(temporary, target / "status.json")
        directory = os.open(target, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if temporary is not None and temporary.exists():
            temporary.unlink()


def main():
    """接收固定阶段和结果，不接收任意错误文本。"""
    parser = argparse.ArgumentParser()
    parser.add_argument("status_dir")
    parser.add_argument("backup_root")
    parser.add_argument("state", choices=("running", "succeeded", "failed"))
    parser.add_argument("started_at")
    parser.add_argument("stage", choices=("initialize", "stop", "apps-freeze", "apps-restore", "database", "files", "verify", "publish", "prune", "restart", "completed"))
    publish(parser.parse_args())


if __name__ == "__main__":
    main()
