"""迁移包的离线校验与已有数据保护回归；不需要 Docker。"""
from contextlib import redirect_stdout
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
        for name in ("images.tar", "compose.json", "data/postgres.dump", "deploy/backup.sh", "deploy/backup_status.py", "deploy/migrate_host.py", "deploy/nginx/ssh.conf.template"):
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

    # test_bundle_permissions 网络服务须可直接执行，凭据和其他迁移文件保持私有。
    def test_bundle_permissions(self):
        binary = self.bundle / "deploy/stacknest-egress"
        binary.write_bytes(b"fixture-program")
        modes = {}
        original = Path.chmod
        # record 在 Windows 同样核对目标模式，Linux 额外检查实际文件权限。
        def record(path, mode, **kwargs):
            modes[path.relative_to(self.bundle).as_posix()] = mode
            return original(path, mode, **kwargs)
        with patch.object(Path, "chmod", record):
            files = migration.bundle_files(self.bundle)
        self.assertEqual(modes["deploy/stacknest-egress"], 0o700)
        self.assertTrue(all(mode == 0o600 for name, mode in modes.items() if name != "deploy/stacknest-egress"))
        self.assertEqual(files["deploy/stacknest-egress"], migration.digest(binary))
        if migration.sys.platform.startswith("linux"):
            self.assertEqual(binary.stat().st_mode & 0o777, 0o700)
            self.assertEqual((self.bundle / "compose.json").stat().st_mode & 0o777, 0o600)

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
        mounts.append({"type": "bind", "source": "/run/stacknest-egress", "target": "/run/stacknest-egress", "read_only": True})
        copied = migration.portable_config(config, self.bundle)
        self.assertEqual(copied["services"]["runner"]["volumes"][-1]["source"], "/run/stacknest-egress")
        config["services"]["stacknest"]["volumes"] = [dict(mounts[-1])]
        copied = migration.portable_config(config, self.bundle)
        self.assertTrue(copied["services"]["stacknest"]["volumes"][0]["read_only"])
        config["services"]["stacknest"]["volumes"][0]["read_only"] = False
        with self.assertRaises(ValueError): migration.portable_config(config, self.bundle)
        config["services"]["stacknest"]["volumes"] = []
        mounts[-1]["read_only"] = False
        with self.assertRaises(ValueError): migration.portable_config(config, self.bundle)
        mounts.pop()
        mounts[0]["source"] = "/other/socket"
        with self.assertRaises(ValueError): migration.portable_config(config, self.bundle)

    # test_tunnel_templates 公网与 SSH 配置须分别导出，禁止迁移 Tunnel 凭据或可写模板挂载。
    def test_tunnel_templates(self):
        ssh = self.root / "ssh.conf"
        public = self.root / "public.conf.template"
        ssh.write_text("SSH local HTTP", encoding="utf-8")
        public.write_text("Cloudflare public HTTPS", encoding="utf-8")
        config = {"volumes": {"postgres-data": {}, "stacknest-data": {}},
                  "services": {name: {"image": "fixture"} for name in migration.SERVICES | {"tunnel"}}}
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

    # test_missing_host_policy 缺少目标机规则时不创建目录或装载镜像。
    def test_missing_host_policy(self):
        config = {"volumes": {"postgres-data": {}, "stacknest-data": {}, "runner-control": {}},
                  "services": {name: {"image": "fixture"} for name in migration.SERVICES | {"runner"}}}
        config["services"]["runner"]["environment"] = {"STACKNEST_APP_EGRESS_POLICY": "/run/stacknest-egress/status.json"}
        migration.write_json(self.bundle / "compose.json", config)
        self.manifest()
        target = self.root / "new-instance"
        outputs = [json.dumps({"Os": "linux", "Arch": "amd64"}),
                   json.dumps({"DockerRootDir": str(self.root)}),
                   RuntimeError("host policy unavailable")]
        with patch.object(migration, "ensure_empty"), patch.object(migration, "run", side_effect=outputs) as run:
            with self.assertRaisesRegex(RuntimeError, "host policy unavailable"):
                migration.import_bundle(argparse.Namespace(bundle=str(self.bundle), destination=str(target), project="demo", preview_port=18080))
            self.assertFalse(target.exists())
            self.assertEqual(run.call_count, 3)
            self.assertEqual(run.call_args.args[0][-1], "check")

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


    # test_single_entry_import 覆盖完整导入成功和启动失败，检查只有回环端口及准确的停止对象。
    def test_single_entry_import(self):
        config = {"volumes": {"postgres-data": {}, "stacknest-data": {}},
                  "services": {name: {"image": "fixture"} for name in migration.SERVICES}}
        config["services"]["nginx"]["ports"] = [{"target": 443, "published": "443"}]
        migration.write_json(self.bundle / "compose.json", config)
        self.manifest()
        for failed in [False, True]:
            target = self.root / ("failed" if failed else "imported")
            calls = []

            # fake_run 保留真实文件生成，替换 Docker 进程并模拟恢复后启动失败。
            def fake_run(args, **kwargs):
                calls.append([str(arg) for arg in args])
                if args[:2] == ["docker", "version"]:
                    return json.dumps({"Os": "linux", "Arch": "amd64"})
                if args[:2] == ["docker", "info"]:
                    return json.dumps({"DockerRootDir": str(self.root)})
                if kwargs.get("stage") == "start restored services" and failed:
                    raise RuntimeError("start failure")
                if args[-1] == "SELECT count(*) FROM pg_tables WHERE schemaname='public'":
                    return "0\n"
                return ""

            with patch.object(migration, "ensure_empty"), patch.object(migration, "run", side_effect=fake_run):
                with redirect_stdout(io.StringIO()):
                    args = argparse.Namespace(bundle=str(self.bundle), destination=str(target), project="demo", preview_port=19080)
                    if failed:
                        with self.assertRaisesRegex(RuntimeError, "start failure"):
                            migration.import_bundle(args)
                    else:
                        migration.import_bundle(args)
            local = json.loads((target / "compose.json").read_text())
            public = json.loads((target / "compose.public.json").read_text())
            self.assertEqual(local["services"]["nginx"]["ports"], [{"target": 82, "published": "19080", "host_ip": "127.0.0.1", "protocol": "tcp"}])
            self.assertEqual(public["services"]["nginx"]["ports"], config["services"]["nginx"]["ports"])
            self.assertTrue((target / "deploy/nginx/ssh.conf.template").is_file())
            if failed:
                self.assertEqual(calls[-1][-3:], ["stop", "stacknest", "nginx"])
                self.assertFalse((target / "migration-ready.json").exists())
            else:
                self.assertTrue((target / "migration-ready.json").exists())
                self.assertEqual(calls[-1][-4:], ["-T", "nginx", "nginx", "-t"])

    # test_missing_preview_template 不完整单入口包在创建目标和加载镜像前失败。
    def test_missing_preview_template(self):
        config = {"volumes": {"postgres-data": {}, "stacknest-data": {}},
                  "services": {name: {"image": "fixture"} for name in migration.SERVICES}}
        migration.write_json(self.bundle / "compose.json", config)
        (self.bundle / "deploy/nginx/ssh.conf.template").unlink()
        self.manifest()
        target = self.root / "missing-template"
        outputs = [json.dumps({"Os": "linux", "Arch": "amd64"}), json.dumps({"DockerRootDir": str(self.root)})]
        with patch.object(migration, "ensure_empty"), patch.object(migration, "run", side_effect=outputs) as run:
            with self.assertRaisesRegex(ValueError, "lacks SSH preview template"):
                migration.import_bundle(argparse.Namespace(bundle=str(self.bundle), destination=str(target), project="demo", preview_port=18080))
            self.assertFalse(target.exists())
            self.assertEqual(run.call_count, 2)

    # test_single_entry_templates 单入口的三类模板分别保留，拒绝可写配置和额外服务。
    def test_single_entry_templates(self):
        config = {"volumes": {"postgres-data": {}, "stacknest-data": {}},
                  "services": {name: {"image": "fixture"} for name in migration.SERVICES}}
        mounts = []
        for index, target in enumerate(["default.conf.template", "ssh.conf.template", "cloudflare-tunnel.conf.template"]):
            path = self.root / target
            path.write_text("entry-" + str(index), encoding="utf-8")
            mounts.append({"type": "bind", "source": str(path), "target": "/etc/nginx/templates/" + target, "read_only": True})
        config["services"]["nginx"]["volumes"] = mounts
        copied = migration.portable_config(config, self.bundle)
        paths = [self.bundle / m["source"] for m in copied["services"]["nginx"]["volumes"]]
        self.assertEqual([p.read_text() for p in paths], ["entry-0", "entry-1", "entry-2"])
        self.assertEqual(len(set(paths)), 3)
        mounts[0]["read_only"] = False
        with self.assertRaises(ValueError): migration.portable_config(config, self.bundle)
        config["services"]["unexpected"] = {"image": "fixture"}
        with self.assertRaises(ValueError): migration.check_layout(config)

    # test_preview_ports 新布局恢复时仅开放 SSH 回环端口，保留原公网配置及输入对象。
    def test_preview_ports(self):
        config = {"services": {"nginx": {"ports": [{"target": 80, "published": "80"}, {"target": 443, "published": "443"}]}}}
        before = json.dumps(config, sort_keys=True)
        public, local = migration.preview_configs(config, 18090)
        self.assertEqual(public, config)
        self.assertEqual(json.dumps(config, sort_keys=True), before)
        ports = local["services"]["nginx"]["ports"]
        self.assertEqual(ports, [{"target": 82, "published": "18090", "host_ip": "127.0.0.1", "protocol": "tcp"}])
        self.assertTrue(local["services"]["nginx"]["volumes"][0]["read_only"])
        for port in [0, -1, 65536]:
            with self.assertRaises(ValueError): migration.preview_configs(config, port)

    # test_legacy_preview 旧包仍使用原 tunnel 的 80 端口，恢复后关闭原公网映射。
    def test_legacy_preview(self):
        config = {"services": {"nginx": {"ports": [{"target": 443, "published": "443"}]},
                               "tunnel": {"ports": [{"target": 81, "published": "18081"}]}}}
        public, local = migration.preview_configs(config, 18090)
        self.assertNotIn("ports", local["services"]["nginx"])
        self.assertEqual(local["services"]["tunnel"]["ports"][0]["target"], 80)
        self.assertEqual(local["services"]["tunnel"]["ports"][0]["host_ip"], "127.0.0.1")
        self.assertEqual(public["services"]["nginx"], config["services"]["nginx"])


if __name__ == "__main__":
    unittest.main()
