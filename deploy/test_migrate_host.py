"""迁移包的离线校验与已有数据保护回归；不需要 Docker。"""
import argparse
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import migrate_host as migration


class MigrationTests(unittest.TestCase):
    # setUp 为每个场景创建独立的临时包，不涉及真实实例。
    def setUp(self):
        self.work = tempfile.TemporaryDirectory()
        self.addCleanup(self.work.cleanup)
        self.root = Path(self.work.name)
        self.bundle = self.root / "bundle"
        self.bundle.mkdir()
        for name in ("images.tar", "compose.json", "data/postgres.dump", "deploy/backup.sh", "deploy/backup_status.py", "deploy/migrate_host.py"):
            path = self.bundle / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"fixture")
        self.archive()
        self.manifest()

    # archive 生成真实 tar 数据，覆盖越界路径和链接场景。
    def archive(self, name="sites/demo/index.html", kind=tarfile.REGTYPE):
        with tarfile.open(self.bundle / "data/files.tar.gz", "w:gz") as archive:
            item = tarfile.TarInfo(name)
            item.type = kind
            item.size = 5 if kind == tarfile.REGTYPE else 0
            item.linkname = "/etc/passwd" if kind == tarfile.SYMTYPE else ""
            archive.addfile(item, io.BytesIO(b"hello") if item.size else None)

    # manifest 对当前测试包生成完整清单，摘要损坏另行模拟。
    def manifest(self):
        migration.write_json(self.bundle / "manifest.json", {
            "format": migration.FORMAT, "platform": "linux/amd64",
            "files": {path.relative_to(self.bundle).as_posix(): migration.digest(path)
                      for path in self.bundle.rglob("*") if path.is_file() and path.name != "manifest.json"},
        })

    # test_integrity 不接受摘要不符、额外文件或缺少文件的迁移包。
    def test_integrity(self):
        self.assertEqual(migration.verify_bundle(self.bundle)["platform"], "linux/amd64")
        (self.bundle / "images.tar").write_bytes(b"tampered")
        with self.assertRaises(ValueError): migration.verify_bundle(self.bundle)
        self.manifest()
        (self.bundle / "data/manifest.json").write_text("extra")
        with self.assertRaises(ValueError): migration.verify_bundle(self.bundle)
        (self.bundle / "data/manifest.json").unlink()
        (self.bundle / "data/postgres.dump").unlink()
        with self.assertRaises(ValueError): migration.verify_bundle(self.bundle)

    # test_unsafe_archives 拒绝站点文件逃逸、符号链接及设备文件。
    def test_unsafe_archives(self):
        for name, kind in [("sites/../../etc/passwd", tarfile.REGTYPE), ("/sites/file", tarfile.REGTYPE), ("etc/passwd", tarfile.REGTYPE), ("sites/link", tarfile.SYMTYPE), ("sites/device", tarfile.CHRTYPE)]:
            with self.subTest(name=name, kind=kind):
                self.archive(name, kind)
                self.manifest()
                with self.assertRaises(ValueError): migration.verify_bundle(self.bundle)

    # test_draft_archive 迁移包包括已验证的待发布版，保留独立预览内容。
    def test_draft_archive(self):
        self.archive("drafts/demo/dep-95d8840a-27a1-42c9-a3a2-7686e9770874/index.html")
        self.manifest()
        self.assertEqual(migration.verify_bundle(self.bundle)["platform"], "linux/amd64")

    # test_application_links 允许数据目录内相对链接，拒绝跨应用和链接祖先写入。
    def test_application_links(self):
        prefix = "apps/0c2d84bb-15a1-492b-938a-83282600c112/data/"
        for link, valid in [("target", True), ("../outside", False), ("/etc/passwd", False)]:
            with tarfile.open(self.bundle / "data/files.tar.gz", "w:gz") as archive:
                item = tarfile.TarInfo(prefix + "link")
                item.type, item.linkname = tarfile.SYMTYPE, link
                archive.addfile(item)
            if valid: self.assertEqual(migration.files_size(self.bundle / "data/files.tar.gz"), 0)
            else:
                with self.assertRaises(ValueError): migration.files_size(self.bundle / "data/files.tar.gz")
        with tarfile.open(self.bundle / "data/files.tar.gz", "w:gz") as archive:
            link = tarfile.TarInfo(prefix + "link")
            link.type, link.linkname = tarfile.SYMTYPE, "target"
            archive.addfile(link)
            entry = tarfile.TarInfo(prefix + "link/file")
            archive.addfile(entry)
        with self.assertRaises(ValueError): migration.files_size(self.bundle / "data/files.tar.gz")

    # test_runner_layout 只允许运行器访问固定 Docker socket，迁移不带旧机器的控制卷。
    def test_runner_layout(self):
        config = {"volumes": {"postgres-data": {}, "stacknest-data": {}, "runner-control": {}},
                  "services": {name: {"image": "fixture"} for name in migration.SERVICES | {"runner"}}}
        mounts = [{"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"},
                  {"type": "bind", "source": "/var/lib/docker", "target": "/docker-storage", "read_only": True}]
        config["services"]["runner"]["volumes"] = mounts
        copied = migration.portable_config(config, self.bundle)
        self.assertEqual(set(copied["volumes"]), set(config["volumes"]))
        mounts[0]["source"] = "/other/socket"
        with self.assertRaises(ValueError): migration.portable_config(config, self.bundle)

    # test_tunnel_templates 公网与 SSH 配置须分别导出，禁止迁移 Tunnel 凭据或可写模板挂载。
    def test_tunnel_templates(self):
        ssh = self.root / "ssh.conf"
        public = self.root / "public.conf.template"
        ssh.write_text("SSH local HTTP", encoding="utf-8")
        public.write_text("Cloudflare public HTTPS", encoding="utf-8")
        config = {"volumes": {"postgres-data": {}, "stacknest-data": {}},
                  "services": {name: {"image": "fixture"} for name in migration.SERVICES}}
        mounts = [
            {"type": "bind", "source": str(ssh), "target": "/etc/nginx/conf.d/default.conf", "read_only": True},
            {"type": "bind", "source": str(public), "target": "/etc/nginx/templates/cloudflare-tunnel.conf.template", "read_only": True},
        ]
        config["services"]["tunnel"]["volumes"] = mounts
        copied = migration.portable_config(config, self.bundle)
        paths = [self.bundle / item["source"] for item in copied["services"]["tunnel"]["volumes"]]
        self.assertEqual([path.read_text(encoding="utf-8") for path in paths], ["SSH local HTTP", "Cloudflare public HTTPS"])
        self.assertNotEqual(paths[0], paths[1])
        for mount in [
            {"type": "bind", "source": str(public), "target": "/etc/nginx/templates/cloudflare-tunnel.conf.template", "read_only": False},
            {"type": "bind", "source": str(public), "target": "/etc/cloudflared/tunnel-token", "read_only": True},
        ]:
            config["services"]["tunnel"]["volumes"] = [mount]
            with self.assertRaises(ValueError):
                migration.portable_config(config, self.bundle)

    # test_existing_instance 不允许覆盖停止的容器、未挂载的数据卷或已有目录。
    def test_existing_instance(self):
        for outputs in [("container-id",), ("", "demo_postgres-data"), ("", "", "orphan-volume")]:
            with patch.object(migration, "run", side_effect=outputs):
                with self.assertRaises(ValueError): migration.ensure_empty("demo")
        with patch.object(migration, "run") as run:
            with self.assertRaises(ValueError):
                migration.import_bundle(argparse.Namespace(bundle=str(self.bundle), destination=str(self.root), project="demo", preview_port=18080))
            run.assert_not_called()

    # test_different_image_stores 在创建目标目录或加载镜像前拒绝不兼容的存储后端。
    def test_different_image_stores(self):
        manifest = json.loads((self.bundle / "manifest.json").read_text(encoding="utf-8"))
        manifest["imageStore"] = "classic"
        migration.write_json(self.bundle / "manifest.json", manifest)
        target = self.root / "new-instance"
        outputs = [json.dumps({"Os": "linux", "Arch": "amd64"}),
                   json.dumps({"DockerRootDir": str(self.root), "DriverStatus": [["driver-type", "io.containerd.snapshotter.v1"]]})]
        with patch.object(migration, "ensure_empty"), patch.object(migration, "run", side_effect=outputs) as run:
            with self.assertRaisesRegex(ValueError, "image stores differ"):
                migration.import_bundle(argparse.Namespace(bundle=str(self.bundle), destination=str(target), project="demo", preview_port=18080))
            self.assertFalse(target.exists())
            self.assertEqual(run.call_count, 2)

    # test_portable_config 移除固定项目名，保留 Compose JSON 中已经转义的美元符号。
    def test_portable_config(self):
        config = {"name": "old", "volumes": {"postgres-data": {"name": "old_db"}, "stacknest-data": {}},
                  "networks": {"default": {"name": "old_default"}},
                  "services": {name: {"image": "fixture", "container_name": name} for name in migration.SERVICES}}
        config["services"]["postgres"]["environment"] = {"POSTGRES_PASSWORD": "a$${UNCHANGED}$$b"}
        copied = migration.portable_config(config, self.bundle)
        self.assertNotIn("name", copied)
        self.assertNotIn("name", copied["networks"]["default"])
        self.assertNotIn("container_name", copied["services"]["postgres"])
        self.assertEqual(copied["services"]["postgres"]["environment"]["POSTGRES_PASSWORD"], "a$${UNCHANGED}$$b")
        self.assertEqual(config["name"], "old")
        config["services"]["nginx"]["volumes"] = [{"type": "bind", "source": str(self.root), "target": "/unknown"}]
        with self.assertRaises(ValueError): migration.portable_config(config, self.bundle)


if __name__ == "__main__":
    unittest.main()
