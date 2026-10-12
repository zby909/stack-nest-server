"""验证发布包不会携带实例秘密，且摘要固定的 Compose 在所有入口均可解析。"""
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

import package_release

ROOT = Path(__file__).resolve().parents[1]
DOCKER = shutil.which("docker")


class PackageReleaseTests(unittest.TestCase):
    # setUp 使用真实公开材料搭建临时工作区，私有文件仅包含测试标记。
    def setUp(self):
        self.work = tempfile.TemporaryDirectory()
        self.addCleanup(self.work.cleanup)
        self.base = Path(self.work.name)
        self.source = self.base / "source"
        self.source.mkdir()
        for name in (*package_release.FILES, ".env.compose.example", "docs/INSTALL_RELEASE.md"):
            target = self.source / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / name, target)
        for name in (".env", ".env.local", "docs/INSTANCE.md", "cert/private.key", "compose.json"):
            target = self.source / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("DO_NOT_SHIP_LOCAL_SECRET", encoding="utf-8")
        self.binary = self.base / "egress"
        self.binary.write_bytes(b"\x7fELF\x02\x01" + b"\x00" * 12 + b"\x3e\x00" + b"fake-code")
        self.manifest = {
            "format": "stacknest-release-v1", "version": "v1.2.3-rc.1", "platform": "linux/amd64",
            "sources": {name: {"repository": "example/stack-nest-" + name, "commit": "a" * 40}
                        for name in ("server", "web")},
            "images": {name: "ghcr.io/example/stacknest-" + name + "@sha256:" + str(index) * 64
                       for index, name in enumerate(("app", "web", "migrate"), 1)},
        }

    # build 生成真实压缩包，并返回包内文件供行为断言。
    def build(self, output="out"):
        archive = package_release.package_release(self.source, self.base / output, self.manifest, self.binary)
        with tarfile.open(archive) as package:
            files = {member.name: package.extractfile(member).read() for member in package.getmembers()}
        return archive, files

    # test_public_contents 校验白名单、每个文件的校验和、镜像摘要及来源记录。
    def test_public_contents(self):
        archive, files = self.build()
        expected = set(package_release.FILES) | {".env.compose.example", "README.md", "bin/stacknest-egress", "release.json", "SHA256SUMS"}
        self.assertEqual(set(files), {"server/" + name for name in expected})
        self.assertFalse(any(b"DO_NOT_SHIP_LOCAL_SECRET" in value for value in files.values()))
        self.assertEqual(json.loads(files["server/release.json"]), self.manifest)
        for line in files["server/SHA256SUMS"].decode().splitlines():
            checksum, name = line.split("  ", 1)
            self.assertEqual(hashlib.sha256(files["server/" + name]).hexdigest(), checksum)
        self.assertEqual((archive.parent / (archive.name + ".sha256")).read_text().split()[0], hashlib.sha256(archive.read_bytes()).hexdigest())
        settings = files["server/.env.compose.example"].decode()
        self.assertIn("COMPOSE_FILE=compose.release.yaml:compose.tls.yaml", settings)
        for value in self.manifest["images"].values():
            self.assertIn(value, settings)
        self.assertIn(b"https://github.com/example/stack-nest-server/blob/" + b"a" * 40, files["server/README.md"])

    # test_reproducible_and_no_overwrite 相同输入完全一致，已有发布包不能被静默覆盖。
    def test_reproducible_and_no_overwrite(self):
        first, _ = self.build()
        second, _ = self.build("second")
        self.assertEqual(first.read_bytes(), second.read_bytes())
        with self.assertRaises(FileExistsError):
            self.build()

    # test_invalid_inputs 拒绝路径注入、浮动镜像、错组件和错误平台二进制。
    def test_invalid_inputs(self):
        for section, field, value in [
            (None, "version", "../../v1"),
            ("images", "app", "ghcr.io/example/stacknest-app:latest"),
            ("images", "app", self.manifest["images"]["web"]),
        ]:
            original = copy.deepcopy(self.manifest)
            with self.subTest(field=field, value=value):
                (self.manifest if section is None else self.manifest[section])[field] = value
                with self.assertRaises(ValueError):
                    self.build()
            self.manifest = original
        self.binary.write_bytes(b"not-a-linux-binary")
        with self.assertRaises(ValueError):
            self.build()

    # test_missing_public_input 缺少入口模板时拒绝生成残缺的部署包。
    def test_missing_public_input(self):
        (self.source / "deploy/nginx/ssh.conf.template").unlink()
        with self.assertRaises(ValueError):
            self.build()

    # test_packaged_compose 使用包内真实文件解析摘要镜像及全部入口，检查挂载依赖完整。
    @unittest.skipUnless(DOCKER, "Docker CLI is required; daemon is not needed")
    def test_packaged_compose(self):
        _, files = self.build()
        unpacked = self.base / "unpacked"
        for name, value in files.items():
            target = unpacked / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(value)
        folder = unpacked / "server"
        env = {key: value for key, value in os.environ.items() if not key.startswith(("STACKNEST_", "COMPOSE_"))}
        for entry in ("tls", "cloudflare", "ssh", "tunnel-public"):
            command = [DOCKER, "compose", "--env-file", ".env.compose.example", "-p", "package-check",
                       "-f", "compose.release.yaml", "-f", "compose." + entry + ".yaml",
                       "-f", "compose.quota.yaml", "-f", "compose.egress.yaml", "config", "--format", "json"]
            result = subprocess.run(command, cwd=folder, env=env, capture_output=True, text=True, timeout=30)
            self.assertEqual(result.returncode, 0, "Packaged Compose failed: " + entry)
            services = json.loads(result.stdout)["services"]
            for service, image in (("runner", "app"), ("stacknest", "app"), ("nginx", "web"), ("migrate", "migrate")):
                self.assertEqual(services[service]["image"], self.manifest["images"][image])
                self.assertNotIn("build", services[service])
            for volume in services["nginx"]["volumes"]:
                if volume["target"].startswith("/etc/nginx/templates/"):
                    self.assertTrue(Path(volume["source"]).is_file(), volume["source"])
