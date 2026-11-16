"""检查演练配置不会继承线上连接，以及损坏备份在开始前被拒绝。"""
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

import restore_backup as drill


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
                    (backup / "postgres.dump").write_bytes(b"incomplete")
                    with self.assertRaisesRegex(ValueError, "备份校验失败"):
                        drill.verify_backup(backup)
                else:
                    with self.assertRaisesRegex(ValueError, "unsafe path"):
                        drill.verify_backup(backup)
