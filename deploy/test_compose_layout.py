"""验证实际 Compose 合并结果，防止可选入口意外暴露公网端口。"""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
DOCKER = shutil.which("docker")

@unittest.skipUnless(DOCKER, "Docker CLI is required; daemon is not needed")
class ComposeLayoutTests(unittest.TestCase):
    # config 使用占位配置解析真实 Compose，不读取部署凭据或连接 Docker daemon。
    def config(self, base, extra=None):
        values = {
            "STACKNEST_ADMIN_DOMAIN": "stack-nest.example.com",
            "STACKNEST_BASE_DOMAIN": "example.com",
            "STACKNEST_POSTGRES_PASSWORD": "compose_test_only",
            "STACKNEST_DATABASE_URL": "postgres://stacknest:compose_test_only@postgres:5432/stacknest?sslmode=disable",
            "STACKNEST_AUTH_ADMIN_EMAIL": "admin@example.com",
            "STACKNEST_AUTH_SECRET": "a" * 48,
            "STACKNEST_SMTP_USER": "smtp@example.com",
            "STACKNEST_SMTP_PASSWORD": "smtp_test_only",
            "STACKNEST_TLS_CERT_DIR": "./cert",
            "STACKNEST_ORIGIN_CERT_DIR": "./cert",
            "STACKNEST_RELEASE": "layout-test",
        }
        env = {key: value for key, value in os.environ.items() if not key.startswith(("STACKNEST_", "COMPOSE_"))}
        env.update(values)
        with tempfile.TemporaryDirectory() as work:
            path = Path(work) / "test.env"
            path.write_text("\n".join(key + "=" + value for key, value in values.items()), encoding="utf-8")
            command = [DOCKER, "compose", "--env-file", str(path), "-p", "stacknest-layout-test", "-f", base]
            if extra:
                command += ["-f", extra]
            result = subprocess.run(command + ["config", "--format", "json"], cwd=ROOT, env=env, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, "Compose configuration could not be parsed")
        return json.loads(result.stdout)

    # test_entry_modes 公网 TLS、本机 SSH 与 CF 回源必须都保持单 Nginx，回环模式不能留下公网映射。
    def test_entry_modes(self):
        for base in ["compose.yaml", "compose.release.yaml"]:
            for extra, expected in [
                (None, {("0.0.0.0", "80", 80)}),
                ("compose.tls.yaml", {("0.0.0.0", "80", 80), ("0.0.0.0", "443", 443)}),
                ("compose.cloudflare.yaml", {("0.0.0.0", "80", 80), ("0.0.0.0", "443", 443)}),
                ("compose.ssh.yaml", {("127.0.0.1", "18080", 82)}),
                ("compose.tunnel-public.yaml", {("127.0.0.1", "18080", 82), ("127.0.0.1", "18081", 81)}),
            ]:
                with self.subTest(base=base, entry=extra):
                    config = self.config(base, extra)
                    services = config["services"]
                    self.assertEqual(set(services), {"postgres", "migrate", "stacknest", "nginx", "runner"})
                    ports = {(p.get("host_ip", "0.0.0.0"), str(p["published"]), p["target"]) for p in services["nginx"]["ports"]}
                    self.assertEqual(ports, expected)
                    self.assertFalse(services["postgres"].get("ports"))
                    self.assertFalse(services["runner"].get("ports"))
                    self.assertFalse(services["stacknest"].get("ports"))
                    for service in ["nginx", "stacknest"]:
                        self.assertFalse(any(m["target"] == "/var/run/docker.sock" for m in services[service].get("volumes", [])))
                    self.assertEqual(services["migrate"]["restart"], "no")
                    self.assertIn("STACKNEST_AUTH_SUPER_ADMIN_EMAIL", services["stacknest"]["environment"])
                    templates = [m for m in services["nginx"]["volumes"] if m["target"].startswith("/etc/nginx/")]
                    self.assertTrue(all(m.get("read_only") for m in templates))
                    if base == "compose.release.yaml":
                        self.assertTrue(all("build" not in s for s in services.values()))
