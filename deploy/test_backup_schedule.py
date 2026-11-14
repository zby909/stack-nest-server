"""验证首次备份与定时任务的启用顺序，测试不操作真实系统服务。"""
from contextlib import nullcontext
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import backup_schedule as schedule


class BackupScheduleTests(unittest.TestCase):
    # test_retry_after_failed_first_backup 第一次失败后同样的命令可以重试，成功后幂等返回。
    def test_retry_after_failed_first_backup(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            source = root / "deployment $ team%.env"
            active, calls = False, []

            # systemctl 模拟 systemd 并记录调用次序。
            def systemctl(command, stage, **_options):
                nonlocal active
                operation = command[1]
                calls.append(operation)
                if operation == "start" and calls.count("start") == 1:
                    raise RuntimeError(stage)
                if operation == "enable":
                    active = True
                return b""

            with patch.object(schedule, "SYSTEMD", root), patch.object(schedule, "install_lock", return_value=nullcontext()), \
                    patch.object(schedule, "run", side_effect=systemctl), \
                    patch.object(schedule, "enabled", side_effect=lambda _unit: active), patch("sys.stdout", new_callable=io.StringIO):
                with self.assertRaisesRegex(RuntimeError, "首次备份失败"):
                    schedule.install_backup(source, True, "fixture", root / "backups", 7)
                self.assertNotIn("enable", calls)
                schedule.install_backup(source, True, "fixture", root / "backups", 7)
                self.assertEqual([op for op in calls if op != "show"], ["daemon-reload", "start", "daemon-reload", "start", "enable"])
                schedule.install_backup(source, True, "fixture", root / "backups", 7)
                self.assertEqual(calls.count("start"), 2)
                with self.assertRaisesRegex(ValueError, "已有其他配置"):
                    schedule.install_backup(source, True, "another", root / "backups", 7)
            unit = (root / "stacknest-backup.service").read_text("utf-8")
            self.assertIn("deployment $$ team%%.env", unit)
            self.assertIn('"--keep" "7"', unit)


if __name__ == "__main__":
    unittest.main()
