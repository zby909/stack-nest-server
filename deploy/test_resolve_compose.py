"""验证失败保留旧 Compose 文件、私有权限及覆盖文件参数。"""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import resolve_compose


class ResolveTests(unittest.TestCase):
    # setUp 为每项测试创建私有快照，完全替换 Docker CLI，不读取真实部署配置。
    def setUp(self):
        self.work = tempfile.TemporaryDirectory()
        self.addCleanup(self.work.cleanup)
        self.root = Path(self.work.name)
        self.target = self.root / "compose.resolved.json"
        self.target.write_text("previous-valid-config\n", encoding="utf-8")

    # test_failure_keeps_snapshot Docker 失败、非法 JSON、空服务均不破坏备份使用的旧配置。
    def test_failure_keeps_snapshot(self):
        for result in [
            subprocess.CompletedProcess([], 1, "", "secret-do-not-print"),
            subprocess.CompletedProcess([], 0, "not-json", ""),
            subprocess.CompletedProcess([], 0, '{"services": {}}', ""),
        ]:
            with self.subTest(result=result.returncode, body=result.stdout), patch.object(resolve_compose.subprocess, "run", return_value=result):
                with self.assertRaises((ValueError, RuntimeError)):
                    resolve_compose.resolve(self.target)
            self.assertEqual(self.target.read_text(), "previous-valid-config\n")
            self.assertEqual(list(self.root.glob(".compose-resolved-*")), [])

    # test_atomic_success 保留原始美元符号转义及入口覆盖，目标权限在 Linux 必须为 0600。
    def test_atomic_success(self):
        raw = json.dumps({"name": "stacknest", "services": {"nginx": {"environment": {"FIXTURE": "a$$LITERAL$$b"}}}})
        result = subprocess.CompletedProcess([], 0, raw, "")
        for project in ["stacknest", "x"]:
            with self.subTest(project=project), patch.object(resolve_compose.subprocess, "run", return_value=result) as run:
                self.assertEqual(resolve_compose.resolve(self.target, project=project), self.target)
                self.assertEqual(run.call_args.args[0], ["docker", "compose", "-p", project, "config", "--format", "json"])
        self.assertEqual(self.target.read_text(encoding="utf-8"), raw + "\n")
        self.assertEqual(list(self.root.glob(".compose-resolved-*")), [])
        if os.name == "posix":
            self.assertEqual(self.target.stat().st_mode & 0o777, 0o600)

    # test_replace_failure_keeps_snapshot 原子替换失败仍保留旧文件，不留下含秘密的临时文件。
    def test_replace_failure_keeps_snapshot(self):
        result = subprocess.CompletedProcess([], 0, '{"services": {"nginx": {}}}', "")
        with patch.object(resolve_compose.subprocess, "run", return_value=result), patch.object(resolve_compose.os, "replace", side_effect=OSError("fixture")):
            with self.assertRaises(OSError):
                resolve_compose.resolve(self.target)
        self.assertEqual(self.target.read_text(), "previous-valid-config\n")
        self.assertEqual(list(self.root.glob(".compose-resolved-*")), [])

    # test_file_options 自定义入口和项目名须保留，不把路径拼接成 shell 命令。
    def test_file_options(self):
        files = [self.root / "compose.yaml", self.root / "compose.tls.yaml"]
        for file in files:
            file.write_text("fixture", encoding="utf-8")
        result = subprocess.CompletedProcess([], 0, '{"services": {"nginx": {}}}', "")
        with patch.object(resolve_compose.subprocess, "run", return_value=result) as run:
            resolve_compose.resolve(self.target, "example", files)
        command = run.call_args.args[0]
        self.assertEqual(command, ["docker", "compose", "-p", "example", "-f", str(files[0].resolve()), "-f", str(files[1].resolve()), "config", "--format", "json"])
        with patch.object(resolve_compose.subprocess, "run") as run:
            with self.assertRaises(ValueError):
                resolve_compose.resolve(self.target, "--invalid-project")
            run.assert_not_called()

    # test_explicit_environment 保留实际部署所用的环境文件，不能悄悄退回默认 .env。
    def test_explicit_environment(self):
        environment = self.root / "deployment.env"
        environment.write_text("FIXTURE=placeholder-only\n", encoding="utf-8")
        result = subprocess.CompletedProcess([], 0, '{"services": {"nginx": {}}}', "")
        with patch.object(resolve_compose.subprocess, "run", return_value=result) as run:
            resolve_compose.resolve(self.target, env_files=[environment])
        self.assertEqual(run.call_args.args[0][:4], ["docker", "compose", "--env-file", str(environment.resolve())])


if __name__ == "__main__":
    unittest.main()
