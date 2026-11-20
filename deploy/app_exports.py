"""从已完成的整机备份发布每个应用自己的持久化文件，供 API 只读下载。"""
import argparse
import contextlib
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import tarfile
import tempfile

BACKUP_NAME = re.compile(r"stacknest-\d{8}T\d{6}Z-\d+")
APP_ID = re.compile(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}")


def private_for_api(path, mode):
    """只给后台运行用户读取权限，目录和文件均不对宿主其他用户开放。"""
    if os.geteuid() == 0:
        os.chown(path, 10001, -1)
    os.chmod(path, mode)


def parts_for(member):
    """只接受固定 UUID 的 data 子树，应用运行记录及其他目录在边界外。"""
    parts = member.name.rstrip("/").split("/")
    if len(parts) < 3 or parts[0] != "apps" or not APP_ID.fullmatch(parts[1]) or parts[2] != "data":
        return None
    return parts[1], parts[3:]


class VerifiedReader:
    """边解压边计算原备份的摘要，避免重复读取整份大文件。"""
    def __init__(self, stream):
        self.stream = stream
        self.digest = hashlib.sha256()

    def read(self, size):
        """记录 tar 解码器实际消费的压缩字节。"""
        block = self.stream.read(size)
        self.digest.update(block)
        return block


def build(source, stage, expected):
    """仅复制目录内的文件与可安全还原的链接。异常条目只影响所属应用。"""
    results, writers, known = {}, {}, {}
    allowance = 0
    with contextlib.ExitStack() as stack:
        reader = VerifiedReader(stack.enter_context(source.open("rb")))
        archive = stack.enter_context(tarfile.open(fileobj=reader, mode="r|gz"))
        for member in archive:
            match = parts_for(member)
            if match is None:
                continue
            app, parts = match
            entry = results.setdefault(app, {"state": "ready"})
            if entry["state"] != "ready":
                continue
            name = "/".join(parts)
            paths = known.setdefault(app, {})
            if (any(p in ("", ".", "..") or "\\" in p or ":" in p or any(ord(c) < 32 for c in p) for p in parts)
                    or name in paths or any(paths.get("/".join(parts[:i]), "dir") != "dir" for i in range(1, len(parts)))
                    or not (member.isfile() or member.isdir() or member.issym() or member.islnk())
                    or (not parts and not member.isdir())):
                entry["state"] = "unsupported"
                continue
            if app not in writers:
                writers[app] = stack.enter_context(tarfile.open(stage / (app + ".tar.gz"), "w:gz", compresslevel=1))
            if not parts:
                continue
            safe = tarfile.TarInfo(name)
            safe.type = member.type if member.islnk() or member.issym() or member.isdir() else tarfile.REGTYPE
            safe.size = member.size if member.isfile() else 0
            if member.issym():
                link = member.linkname
                if not link or link.startswith("/") or ".." in link.split("/") or "\\" in link or ":" in link or any(ord(c) < 32 for c in link):
                    entry["state"] = "unsupported"
                    continue
                safe.linkname = link
            elif member.islnk():
                prefix = "apps/" + app + "/data/"
                if not member.linkname.startswith(prefix) or paths.get(member.linkname[len(prefix):]) != "file":
                    entry["state"] = "unsupported"
                    continue
                safe.linkname = member.linkname[len(prefix):]
            paths[name] = "dir" if member.isdir() else "file" if member.isfile() else "link"
            safe.mode, safe.mtime = member.mode & 0o777, member.mtime
            if allowance < safe.size + 1024:
                space = shutil.disk_usage(stage)
                headroom = space.free - space.total // 10
                if headroom < safe.size + 1024:
                    raise OSError("可用磁盘不足以安全生成应用归档")
                allowance = min(headroom, 32 << 20)
            allowance -= safe.size + 1024
            writers[app].addfile(safe, archive.extractfile(member) if member.isfile() else None)
        while reader.read(1024 * 1024):
            pass
        if reader.digest.hexdigest() != expected:
            raise ValueError("文件归档 SHA-256 校验失败")
    for app, entry in results.items():
        path = stage / (app + ".tar.gz")
        if entry["state"] == "ready":
            private_for_api(path, 0o400)
            entry["bytes"] = path.stat().st_size
        elif path.exists():
            path.unlink()
    return results


def publish(folder, status):
    """校验来源后原子切换索引，已开始的下载可继续读取打开的旧文件。"""
    import fcntl
    if not BACKUP_NAME.fullmatch(folder.name) or folder.is_symlink():
        raise ValueError("无效的备份目录")
    for name in ("format", "metadata", "SHA256SUMS", "files.tar.gz"):
        if (folder / name).is_symlink() or not (folder / name).is_file():
            raise ValueError("备份不完整")
    if (folder / "format").read_text().strip() != "stacknest-backup-v1":
        raise ValueError("不支持该备份格式")
    metadata = dict(line.split("=", 1) for line in (folder / "metadata").read_text().splitlines() if "=" in line)
    instant = dt.datetime.fromisoformat(metadata["created_at"].replace("Z", "+00:00"))
    if instant.tzinfo is None:
        raise ValueError("备份缺少时间")
    digest = next((line.split()[0] for line in (folder / "SHA256SUMS").read_text().splitlines()
                   if len(line.split()) == 2 and line.split()[1] == "files.tar.gz"), "")
    index = status / "app-exports.json"
    with (status / ".app-exports.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        previous = json.loads(index.read_text()) if index.is_file() else {}
        if previous.get("backup") == folder.name or previous.get("createdAt", "") > metadata["created_at"]:
            return
        source = folder / "files.tar.gz"
        stage = Path(tempfile.mkdtemp(prefix="app-export-", dir=status))
        temp = None
        try:
            entries = build(source, stage, digest)
            private_for_api(stage, 0o500)
            data = {"backup": folder.name, "directory": stage.name, "createdAt": metadata["created_at"], "apps": entries}
            with tempfile.NamedTemporaryFile(mode="w", dir=status, delete=False) as out:
                temp = Path(out.name)
                json.dump(data, out)
                out.flush()
                os.fsync(out.fileno())
            private_for_api(temp, 0o400)
            os.replace(temp, index)
        except BaseException:
            stage.chmod(0o700)
            shutil.rmtree(stage)
            raise
        finally:
            if temp:
                temp.unlink(missing_ok=True)
        for old in status.glob("app-export-*"):
            if old != stage and re.fullmatch(r"app-export-[a-z0-9_]{8}", old.name) and not old.is_symlink() and old.is_dir():
                old.chmod(0o700)
                shutil.rmtree(old)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("completed_backup", type=Path)
    parser.add_argument("status_dir", type=Path)
    args = parser.parse_args()
    publish(args.completed_backup, args.status_dir)
