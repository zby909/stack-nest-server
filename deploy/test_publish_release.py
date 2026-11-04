"""验证 GitHub 失败和响应丢失时不会重复发布、覆盖附件或公开不完整版本。"""
import copy
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

import publish_release as release

VERSION = "v1.2.3-rc.1"
REPOSITORY = "example/stack-nest-server"
REVISION = "a" * 40


class FakeGitHub(release.GitHub):
    # __init__ 模拟远端持久状态，故障发生后状态不会随请求错误一起撤销。
    def __init__(self, faults=()):
        super().__init__(REPOSITORY, sleep=lambda _: None)
        self.release = None
        self.faults = list(faults)
        self.writes = []
        self.uploads = []
        self.draft_lookups = 0
        self.tag_revision = REVISION

    # fault 在指定操作前后注入一次 API 错误，区分未执行与仅响应丢失。
    def fault(self, operation, when):
        if self.faults and self.faults[0][:2] == (operation, when):
            raise release.APIError(self.faults.pop(0)[2])

    # raw 维护草稿、附件摘要和发布状态，只模拟 GitHub 对外可观察的行为。
    def raw(self, method, path, payload=None, file=None):
        if method == "GET":
            if "/git/ref/" in path:
                return {"object": {"type": "tag", "sha": "b" * 40}}
            if "/git/tags/" in path:
                return {"object": {"type": "commit", "sha": self.tag_revision}}
            if "/releases/tags/" in path:
                if not self.release or self.release["draft"]:
                    raise release.APIError(404)
                return copy.deepcopy(self.release)
            self.draft_lookups += 1
            return [copy.deepcopy(self.release)] if self.release else []
        operation = "upload" if file else "publish" if method == "PATCH" else "create"
        self.writes.append(operation)
        self.fault(operation, "before")
        if operation == "create":
            if self.release:
                raise release.APIError(422)
            self.release = {**payload, "id": 10, "assets": [], "html_url": "https://example.test/release"}
        elif operation == "upload":
            if any(asset["name"] == file.name for asset in self.release["assets"]):
                raise release.APIError(422)
            data = file.read_bytes()
            self.uploads.append(file.name)
            self.release["assets"].append({"name": file.name, "state": "uploaded", "size": len(data), "digest": "sha256:" + hashlib.sha256(data).hexdigest()})
        else:
            self.release.update(payload)
        self.fault(operation, "after")
        return copy.deepcopy(self.release)


class PublishReleaseTests(unittest.TestCase):
    # setUp 构造七个公开包及校验文件，主包包含实际版本来源清单。
    def setUp(self):
        work = tempfile.TemporaryDirectory()
        self.addCleanup(work.cleanup)
        self.directory = Path(work.name)
        manifest = {
            "version": VERSION, "platform": "linux/amd64",
            "sources": {kind: {"repository": "example/stack-nest-" + kind, "commit": REVISION} for kind in ("server", "web")},
            "images": {kind: "ghcr.io/example/stacknest-" + kind + "@sha256:" + "1" * 64 for kind in ("app", "web", "migrate")},
        }
        data = json.dumps(manifest).encode()
        with tarfile.open(self.directory / f"stacknest-{VERSION}-linux-amd64.tar.gz", "w:gz") as archive:
            info = tarfile.TarInfo("server/release.json")
            info.size = len(data)
            archive.addfile(info, io.BytesIO(data))
        for system in ("linux", "windows", "darwin"):
            for arch in ("amd64", "arm64"):
                suffix = ".zip" if system == "windows" else ".tar.gz"
                (self.directory / f"stacknest-deploy-{VERSION}-{system}-{arch}{suffix}").write_bytes((system + arch).encode())
        for path in list(self.directory.iterdir()):
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            Path(str(path) + ".sha256").write_text(digest + "  " + path.name + "\n")

    # publish 使用生产入口，检查从本地产物到最终公开状态的完整流程。
    def publish(self, client):
        return release.publish(REPOSITORY, VERSION, self.directory, client)

    # test_lost_responses 变更已成功但响应为 502 时，只确认原结果，不重复创建或上传。
    def test_lost_responses(self):
        client = FakeGitHub([("create", "after", 502), ("upload", "after", 502), ("publish", "after", 502)])
        result = self.publish(client)
        self.assertFalse(result["draft"])
        self.assertEqual(len(result["assets"]), 14)
        self.assertEqual(client.writes.count("create"), 1)
        self.assertEqual(client.writes.count("publish"), 1)
        self.assertEqual(len(set(client.uploads)), 14)
        self.assertGreater(client.draft_lookups, 0)

    # test_transient_create_failures 429 与 502 在未产生草稿时会有限重试，之后正常完成。
    def test_transient_create_failures(self):
        client = FakeGitHub([("create", "before", 429), ("create", "before", 502)])
        self.assertFalse(self.publish(client)["draft"])
        self.assertEqual(client.writes.count("create"), 3)

    # test_failure_is_bounded 持续服务端故障最多请求四次，绝不宣称发布成功。
    def test_failure_is_bounded(self):
        client = FakeGitHub([("create", "before", 502)] * 10)
        with self.assertRaises(release.APIError):
            self.publish(client)
        self.assertEqual(client.writes, ["create"] * 4)
        self.assertIsNone(client.release)

    # test_permission_error 不重试鉴权失败，也不公开空版本。
    def test_permission_error(self):
        client = FakeGitHub([("create", "before", 403)])
        with self.assertRaises(release.APIError):
            self.publish(client)
        self.assertEqual(client.writes, ["create"])

    # test_resume_same_draft 上传中断后重跑复用原草稿，已完成附件不重复上传。
    def test_resume_same_draft(self):
        client = FakeGitHub([("upload", "after", 502), ("upload", "before", 403)])
        with self.assertRaises(release.APIError):
            self.publish(client)
        self.assertTrue(client.release["draft"])
        self.assertEqual(len(client.release["assets"]), 1)
        self.publish(client)
        self.assertEqual(len(client.uploads), 14)
        previous = len(client.writes)
        self.publish(client)
        self.assertEqual(len(client.writes), previous)
        self.assertEqual(client.writes.count("create"), 1)

    # test_unrelated_release_same_tag 同名人工草稿或已发布版本均不覆盖。
    def test_unrelated_release_same_tag(self):
        for draft in (True, False):
            with self.subTest(draft=draft):
                client = FakeGitHub()
                client.release = {"tag_name": VERSION, "body": "manual release", "draft": draft, "assets": []}
                with self.assertRaises(ValueError):
                    self.publish(client)
                self.assertEqual(client.writes, [])

    # test_corrupt_remote_asset 附件不匹配或不完整时停止，不删除和覆盖。
    def test_corrupt_remote_asset(self):
        client = FakeGitHub()
        self.publish(client)
        client.release["draft"] = True
        client.release["assets"][0]["digest"] = "sha256:" + "0" * 64
        previous = len(client.writes)
        with self.assertRaises(ValueError):
            self.publish(client)
        self.assertTrue(client.release["draft"])
        self.assertEqual(len(client.writes), previous)

    # test_local_file_validation 校验错误或夹带私有文件时，写入 GitHub 之前就拒绝。
    def test_local_file_validation(self):
        client = FakeGitHub()
        path = next(self.directory.glob("*.sha256"))
        original = path.read_bytes()
        path.write_text("invalid\n")
        with self.assertRaises(ValueError):
            self.publish(client)
        path.write_bytes(original)
        (self.directory / ".env.local").write_text("TEST_SECRET")
        with self.assertRaises(ValueError):
            self.publish(client)
        self.assertEqual(client.writes, [])

    # test_tag_moved 在任何写入之前拒绝已偏离实际构建来源的标签。
    def test_tag_moved(self):
        client = FakeGitHub()
        client.tag_revision = "c" * 40
        with self.assertRaises(ValueError):
            self.publish(client)
        self.assertEqual(client.writes, [])


if __name__ == "__main__":
    unittest.main()
