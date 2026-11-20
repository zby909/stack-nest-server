"""应用下载归档必须只包含它自己的文件，失败不能替换上次副本。"""
import hashlib
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest

import app_exports

A = "11111111-1111-4111-8111-111111111111"
B = "22222222-2222-4222-8222-222222222222"


class AppExportTests(unittest.TestCase):
    @unittest.skipUnless(os.name == "posix", "宿主发布器使用 flock 和 Unix 文件权限")
    def test_isolation_rotation_and_checksum(self):
        """核对跨应用隔离、内部链接、校验失败回退及旧下载句柄。"""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            status = root / "status"
            status.mkdir()
            def source(day, data):
                folder = root / f"stacknest-202611{day}T030000Z-1"
                folder.mkdir()
                with tarfile.open(folder / "files.tar.gz", "w:gz") as out:
                    for path in (f"apps/{A}/data/旅行.txt",f"apps/{B}/data/private",f"apps/{A}/last-deployment-failure.json"):
                        item = tarfile.TarInfo(path)
                        item.size = len(data)
                        out.addfile(item, io.BytesIO(data))
                    for app, target in ((A,"旅行.txt"),(B,"/outside")):
                        item = tarfile.TarInfo(f"apps/{app}/data/link")
                        item.type, item.linkname = tarfile.SYMTYPE, target
                        out.addfile(item)
                (folder / "format").write_text("stacknest-backup-v1")
                (folder / "metadata").write_text(f"created_at=2026-11-{day}T03:00:00Z\n")
                digest = hashlib.sha256((folder / "files.tar.gz").read_bytes()).hexdigest()
                (folder / "SHA256SUMS").write_text(digest+"  files.tar.gz\n")
                return folder
            first = source("19", b"version-one")
            app_exports.publish(first, status)
            def index():
                return json.loads((status / "app-exports.json").read_text())
            old = index()
            folder = status / old["directory"]
            self.assertEqual(old["apps"][B], {"state":"unsupported"})
            self.assertFalse((folder / (B+".tar.gz")).exists())
            with (folder / (A+".tar.gz")).open("rb") as opened:
                second = source("20", b"version-two")
                checksum = second / "SHA256SUMS"
                valid = checksum.read_text()
                checksum.write_text("bad  files.tar.gz")
                with self.assertRaises(ValueError):
                    app_exports.publish(second,status)
                self.assertEqual(index(), old)
                checksum.write_text(valid)
                app_exports.publish(second,status)
                app_exports.publish(first,status)
                self.assertEqual(index()["backup"], second.name)
                self.assertFalse(folder.exists())
                with tarfile.open(fileobj=opened) as content:
                    self.assertEqual(content.getnames(), ["旅行.txt","link"])
                    self.assertEqual(content.extractfile("link").read(), b"version-one")
