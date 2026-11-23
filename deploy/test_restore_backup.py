"""检查演练配置不会继承线上连接，以及损坏备份在开始前被拒绝。"""
import io
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import restore_backup as drill
from backup_config import save_snapshot


class RestoreBackupTests(unittest.TestCase):
    # test_isolated_config 验证外部卷、凭据和出站服务不会被复制到演练实例。
    def test_isolated_config(self):
        source = {
            "name": "production",
            "volumes": {name: {"external": True} for name in ("postgres-data", "stacknest-data", "runner-control")},
            "services": {name: {"image": name + ":original"} for name in
                         ("postgres", "migrate", "stacknest", "runner", "nginx", "tunnel")},
        }
        source["services"]["stacknest"]["environment"] = {
            "STACKNEST_AUTH_SECRET": "original-auth-key",
            "STACKNEST_AUTH_ADMIN_EMAIL": "admin@example.invalid",
            "STACKNEST_DATABASE_URL": "postgres://old-instance",
            "STACKNEST_SMTP_HOST": "production-mail.example.invalid",
            "STACKNEST_SMTP_PASSWORD": "original-smtp",
            "STACKNEST_RUNNER_SOCKET": "/run/production.sock",
        }
        source["services"]["nginx"]["ports"] = [{"target": 443, "published": "443"}]
        config = drill.rehearsal_config(source, 19081)
        self.assertEqual(config["volumes"], {"postgres-data": {}, "stacknest-data": {}})
        self.assertEqual(set(config["services"]), {"postgres", "migrate", "stacknest", "nginx"})
        env = config["services"]["stacknest"]["environment"]
        self.assertEqual(env["STACKNEST_AUTH_SECRET"], "original-auth-key")
        self.assertEqual(env["STACKNEST_SMTP_HOST"], "127.0.0.1")
        self.assertIn("@postgres:5432/stacknest", env["STACKNEST_DATABASE_URL"])
        self.assertIn(env["STACKNEST_DATABASE_URL"], config["services"]["migrate"]["command"])
        self.assertEqual(config["services"]["nginx"]["ports"],
                         [{"target": 82, "published": "19081", "host_ip": "127.0.0.1", "protocol": "tcp"}])
        for production_value in ("production", "original-smtp", "old-instance"):
            self.assertNotIn(production_value, json.dumps(config))

    # test_archive_integrity 校验备份格式、摘要和解压范围全部使用实际文件。
    def test_archive_integrity(self):
        with tempfile.TemporaryDirectory() as path:
            backup = Path(path)
            (backup / "format").write_text("stacknest-backup-v1\n", encoding="utf-8")
            (backup / "postgres.dump").write_bytes(b"database")
            for filename in ("sites/demo/index.html", "../outside"):
                with tarfile.open(backup / "files.tar.gz", "w:gz") as archive:
                    entry = tarfile.TarInfo(filename)
                    entry.size = 5
                    archive.addfile(entry, io.BytesIO(b"hello"))
                (backup / "SHA256SUMS").write_text("".join(
                    f"{drill.digest(backup / name)}  {name}\n" for name in sorted(drill.ARCHIVES)
                ), encoding="utf-8")
                if filename.startswith("sites/"):
                    self.assertEqual(drill.verify_backup(backup), 5)
                    with self.assertRaisesRegex(ValueError, "旧备份"):
                        drill.original_config(backup, None)
                    with patch.object(drill, "run", return_value=b'{"name":"original"}'):
                        self.assertEqual(drill.original_config(backup, backup / "format"), {"name": "original"})
                    (backup / "postgres.dump").write_bytes(b"incomplete")
                    with self.assertRaisesRegex(ValueError, "备份校验失败"):
                        drill.verify_backup(backup)
                else:
                    with self.assertRaisesRegex(ValueError, "unsafe path"):
                        drill.verify_backup(backup)

    # 新备份使用内容编号，换机后可以自动配对，也可校验带来的原配置副本。
    def test_paired_backup(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            folder = root / "backup"
            folder.mkdir()
            first = {"name": "original", "services": {"stacknest": {"environment": {"KEY": "a$$b"}}}}
            other = {"name": "changed", "services": first["services"]}
            identifier = save_snapshot(root, first)
            self.assertNotEqual(save_snapshot(root, other), identifier)
            self.assertEqual(save_snapshot(root, dict(reversed(list(first.items())))), identifier)
            (folder / "format").write_text("stacknest-backup-v2\n")
            (folder / "postgres.dump").write_bytes(b"database")
            with tarfile.open(folder / "files.tar.gz", "w:gz"):
                pass
            metadata = folder / "metadata"
            metadata.write_text(f"created_at=2026-11-23T00:00:00Z\nconfig_sha256={identifier}\nplatform_version=v1.2.3\n")
            names = sorted(drill.ARCHIVES | {"metadata"})
            (folder / "SHA256SUMS").write_text("".join(f"{drill.digest(folder / name)}  {name}\n" for name in names))
            self.assertEqual(drill.verify_backup(folder), 0)
            self.assertEqual(drill.original_config(folder, None), first)
            snapshot = root / ".configs" / (identifier + ".json")
            copy = root / "saved-privately.json"
            snapshot.rename(copy)
            with self.assertRaisesRegex(ValueError, "缺少私有配置"):
                drill.original_config(folder, None)
            self.assertEqual(drill.original_config(folder, copy), first)
            with patch.object(drill, "run", return_value=json.dumps(other).encode()):
                with self.assertRaisesRegex(ValueError, "不一致"):
                    drill.original_config(folder, folder / "format")
            copy.rename(snapshot)
            snapshot.write_bytes(b"{}")
            with self.assertRaisesRegex(ValueError, "校验失败"):
                drill.original_config(folder, None)
            with self.assertRaisesRegex(ValueError, "校验失败"):
                save_snapshot(root, first)
            metadata.write_text("config_sha256=../private\n")
            with self.assertRaisesRegex(ValueError, "备份校验失败"):
                drill.verify_backup(folder)
            with self.assertRaisesRegex(ValueError, "编号无效"):
                drill.original_config(folder, None)

    @unittest.skipUnless(sys.platform.startswith("linux") and shutil.which("docker"), "需要 Linux Docker Compose CLI")
    def test_compose_reads_same_settings_from_snapshot(self):
        """在真实 Compose 中往返相对挂载和含美元符号的值，避免快照改变备份目标。"""
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            source = root / "compose.json"
            source.write_text(json.dumps({"name": "snapshot-test", "services": {"stacknest": {
                "image": "example.invalid/mock:v1", "environment": {"KEY": "cost$$5"},
                "volumes": ["./content:/data"]}}}))
            command = ["docker", "compose", "--env-file", "/dev/null", "-p", "snapshot-test", "-f"]
            current = json.loads(subprocess.check_output(command + [source, "config", "--format", "json"]))
            snapshot = root / ".configs" / (save_snapshot(root, current) + ".json")
            actual = json.loads(subprocess.check_output(command + [snapshot, "config", "--format", "json"]))
            self.assertEqual(current, actual)
            self.assertEqual(actual["services"]["stacknest"]["volumes"][0]["source"], str(root / "content"))
