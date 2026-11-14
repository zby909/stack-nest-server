"""覆盖升级前拒绝、配置保留及失败回退。"""
from contextlib import nullcontext
import copy
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import upgrade_release as upgrade


class UpgradeTests(unittest.TestCase):
    # setUp 只使用演练配置与无效凭据占位。
    def setUp(self):
        work = tempfile.TemporaryDirectory()
        self.addCleanup(work.cleanup)
        self.root = Path(work.name).resolve()
        self.images = {key: f"registry.example/stacknest-{key}@sha256:" + char * 64
                       for key, char in zip(("app", "web", "migrate"), "abc")}
        self.old = {"name": "stacknest", "volumes": {"postgres-data": {}, "stacknest-data": {}, "runner-control": {}},
                    "services": {name: {"image": "old-" + name}
                                 for name in ("stacknest", "runner", "nginx", "migrate", "postgres")}}
        self.old["services"]["nginx"]["ports"] = [{"host_ip": "127.0.0.1", "published": "18080", "target": 82}]
        self.manifest = {"version": "v1.0.0-rc.17", "images": self.images, "sources": {"server": {"commit": "a" * 40}}}

    # test_check_candidate_preserves_baseline 候选校验后仍能发现原配置是否并发变化。
    def test_check_candidate_preserves_baseline(self):
        initial = copy.deepcopy(self.old)
        candidate = json.loads(upgrade.updated_config(self.old, self.images))
        upgrade.ensure_candidate(self.old, candidate, self.images)
        self.assertEqual(self.old, initial)
        candidate["services"]["nginx"]["ports"][0]["host_ip"] = "0.0.0.0"
        with self.assertRaisesRegex(ValueError, "beyond the release images"):
            upgrade.ensure_candidate(self.old, candidate, self.images)

    # test_updated_env_keeps_credentials 镜像替换保留原项目的秘密、覆盖及美元符号。
    def test_updated_env_keeps_credentials(self):
        old = "COMPOSE_FILE=compose.release.yaml:compose.ssh.yaml\r\nSTACKNEST_AUTH_SECRET='x$y'\r\nSTACKNEST_RELEASE=v1.0.0-rc.16\r\n"
        next = upgrade.updated_env(old, self.manifest)
        self.assertIn("STACKNEST_AUTH_SECRET='x$y'\r\n", next)
        self.assertIn("COMPOSE_FILE=compose.release.yaml:compose.ssh.yaml\r\n", next)
        self.assertIn("STACKNEST_RELEASE=v1.0.0-rc.17\r\n", next)
        self.assertNotIn("\n", next.replace("\r\n", ""))
        self.assertIn(self.images["app"], next)
        with self.assertRaisesRegex(ValueError, "duplicate"):
            upgrade.updated_env(old + "STACKNEST_RELEASE=v1.0.0-rc.16\n", self.manifest)

    # test_migrations_gate 迁移内容不同或当前未同步时，禁止切换。
    def test_migrations_gate(self):
        self.old["services"]["migrate"]["command"] = upgrade.MIGRATION_COMMAND + ["test-only"]
        self.old["services"]["stacknest"]["environment"] = {"STACKNEST_DATABASE_URL": "test-only"}
        with patch.object(upgrade, "migration_checksum", side_effect=[b"old", b"new"]), patch.object(upgrade, "run") as run:
            with self.assertRaisesRegex(ValueError, "migration files differ"):
                upgrade.check_migrations([], self.old, self.images["migrate"])
            run.assert_not_called()
        with patch.object(upgrade, "migration_checksum", return_value=b"same"), patch.object(upgrade, "run", side_effect=[b"", b"PENDING 1"]):
            with self.assertRaisesRegex(ValueError, "pending or incomplete"):
                upgrade.check_migrations([], self.old, self.images["migrate"])

    # test_rollback_restores_every_file 验收失败后按原配置重启，连同备份与公网快照一并恢复。
    def test_rollback_restores_every_file(self):
        files = {self.root / name: (("old " + name + "\r\n").encode(), "new " + name)
                 for name in ("compose.json", "compose.public.json", "compose.resolved.json")}
        for path, (original, _) in files.items():
            path.write_bytes(original)
        calls = []

        # start 记录每次启动读取的文件内容。
        def start(_command):
            calls.append({path.name: path.read_bytes() for path in files})

        with patch.object(upgrade, "backup_lock", return_value=nullcontext(None)), \
                patch.object(upgrade, "read_config", return_value=self.old), \
                patch.object(upgrade, "check_idle"), patch.object(upgrade, "backup") as backup, \
                patch.object(upgrade, "run"), patch.object(upgrade, "start", side_effect=start), \
                patch.object(upgrade, "check_health", side_effect=[RuntimeError("fixture failed"), "old"]), \
                patch("sys.stdout", new_callable=io.StringIO):
            with self.assertRaisesRegex(RuntimeError, "fixture failed"):
                upgrade.apply_release(self.root, [], "stacknest", self.old, files, "v1.0.0-rc.16",
                                      self.manifest, self.root, self.root / "status", None)
            backup.assert_called_once()
        self.assertEqual(calls, [{path.name: content.encode() for path, (_, content) in files.items()},
                                 {path.name: original for path, (original, _) in files.items()}])

    # test_failed_backup_recovers_original 备份失败时恢复服务和被冻结的应用，保留原配置。
    def test_failed_backup_recovers_original(self):
        path = self.root / "compose.json"
        path.write_text("original", encoding="utf-8")
        with patch.object(upgrade, "backup_lock", return_value=nullcontext(None)), \
                patch.object(upgrade, "read_config", return_value=self.old), \
                patch.object(upgrade, "check_idle"), \
                patch.object(upgrade, "backup", side_effect=RuntimeError("fixture backup failed")), \
                patch.object(upgrade, "run") as run, patch.object(upgrade, "start") as start, \
                patch.object(upgrade, "check_health") as health, patch("sys.stdout", new_callable=io.StringIO):
            with self.assertRaisesRegex(RuntimeError, "fixture backup failed"):
                upgrade.apply_release(self.root, [], "stacknest", self.old, {path: (b"original", "updated")}, "v1.0.0-rc.16",
                                      self.manifest, self.root, self.root / "status", None)
            start.assert_called_once_with([])
            self.assertIn("apps-thaw", run.call_args.args[0])
            health.assert_called_once_with([], "v1.0.0-rc.16")
        self.assertEqual(path.read_text(encoding="utf-8"), "original")

    # test_backup_keeps_operator_edits 运维在备份期间修改配置时，保留原服务和对方修改。
    def test_backup_keeps_operator_edits(self):
        path = self.root / "compose.json"
        path.write_bytes(b"original")

        # edit 模拟在备份完成时，有人写入了不同的配置。
        def edit(*_args):
            path.write_bytes(b"operator edit")

        with patch.object(upgrade, "backup_lock", return_value=nullcontext(None)), \
                patch.object(upgrade, "read_config", return_value=self.old), \
                patch.object(upgrade, "check_idle"), patch.object(upgrade, "backup", side_effect=edit), \
                patch.object(upgrade, "run") as run, patch("sys.stdout", new_callable=io.StringIO):
            with self.assertRaisesRegex(ValueError, "deployment files changed"):
                upgrade.apply_release(self.root, [], "stacknest", self.old, {path: (b"original", "updated")}, "v1.0.0-rc.16",
                                      self.manifest, self.root, self.root / "status", None)
            run.assert_not_called()
        self.assertEqual(path.read_bytes(), b"operator edit")


if __name__ == "__main__":
    unittest.main()
