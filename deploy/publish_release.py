#!/usr/bin/env python3
"""校验发布材料，在草稿中上传并核对全部附件，有限重试后正式发布。"""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile
import time

from release_manifest import validate


class APIError(RuntimeError):
    # __init__ 仅保留状态码，避免将 CLI 的认证信息和原始响应写入日志。
    def __init__(self, status):
        self.status = status
        self.retryable = status == 0 or status == 429 or status >= 500
        super().__init__(f"GitHub request failed (HTTP {status or 'unknown'})")


class GitHub:
    # __init__ 凭据由 gh 从环境读取，不作为命令参数或本地配置传递。
    def __init__(self, repository, sleep=time.sleep):
        self.repository = repository
        self.sleep = sleep

    # raw 区分 JSON 请求与文件流上传，错误不回显原始响应。
    def raw(self, method, path, payload=None, file=None):
        args = ["gh", "api", "--method", method, path]
        if file:
            args += ["--header", "Content-Type: application/octet-stream", "--input", str(file)]
        elif payload is not None:
            args += ["--input", "-"]
        try:
            result = subprocess.run(args, input=json.dumps(payload) if payload is not None else None,
                                    capture_output=True, text=True, timeout=180)
        except subprocess.TimeoutExpired:
            raise APIError(0) from None
        if result.returncode:
            match = re.search(r"HTTP (\d{3})", result.stderr)
            raise APIError(int(match[1]) if match else 0)
        try:
            return json.loads(result.stdout) if result.stdout.strip() else None
        except json.JSONDecodeError:
            raise APIError(0) from None

    # get 对只读请求有限重试；权限错误和其他确定的客户端错误立即停止。
    def get(self, path):
        for attempt in range(4):
            try:
                return self.raw("GET", path)
            except APIError as failure:
                if not failure.retryable or attempt == 3:
                    raise
                self.sleep(2 ** (attempt + 1))

    # find 同时查找已发布版本和当前账号可见的草稿，不将草稿的 404 当作创建失败。
    def find(self, version):
        root = f"repos/{self.repository}/releases"
        try:
            return self.get(root + "/tags/" + version)
        except APIError as failure:
            if failure.status != 404:
                raise
        page = 1
        while True:
            entries = self.get(root + f"?per_page=100&page={page}")
            match = next((entry for entry in entries if entry["tag_name"] == version), None)
            if match or len(entries) < 100:
                return match
            page += 1

    # ensure 在变更响应丢失时先核对远端结果，避免重复创建或覆盖附件。
    def ensure(self, label, read, write):
        for attempt in range(4):
            if value := read():
                return value
            try:
                write()
                failure = APIError(0)
            except APIError as error:
                failure = error
                if not failure.retryable and failure.status != 422:
                    raise
            if value := read():
                return value
            if not failure.retryable or attempt == 3:
                raise failure
            print(f"{label}: temporary GitHub error; retry {attempt + 1}/3", flush=True)
            self.sleep(2 ** (attempt + 1))


# collect 只接受七个明确的公开版本包及各自校验文件，拒绝夹带私有文件。
def collect(directory, version, repository):
    directory = Path(directory)
    names = {f"stacknest-{version}-linux-amd64.tar.gz"}
    for system in ("linux", "windows", "darwin"):
        for arch in ("amd64", "arm64"):
            names.add(f"stacknest-deploy-{version}-{system}-{arch}" + (".zip" if system == "windows" else ".tar.gz"))
    expected = names | {name + ".sha256" for name in names}
    paths = list(directory.iterdir())
    if {path.name for path in paths} != expected or any(path.is_symlink() or not path.is_file() for path in paths):
        raise ValueError("release directory must contain exactly the seven public packages and their checksums")
    files = {path.name: {"size": path.stat().st_size, "digest": "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()} for path in paths}
    for name in names:
        parts = (directory / (name + ".sha256")).read_text().strip().split("  ")
        if parts != [files[name]["digest"].removeprefix("sha256:"), name]:
            raise ValueError("invalid package checksum: " + name)
    with tarfile.open(directory / f"stacknest-{version}-linux-amd64.tar.gz") as archive:
        manifest = json.load(archive.extractfile("server/release.json"))
    validate(manifest)
    if manifest["version"] != version or manifest["sources"]["server"]["repository"] != repository:
        raise ValueError("package source or version does not match release")
    identity = hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()
    return files, manifest, f"<!-- stacknest-release:{version}:{identity} -->"


# matching 确认草稿属于本次完全相同的构建，附件一旦存在就必须匹配，绝不覆盖。
def matching(release, version, marker, files):
    if not release:
        return None
    if release["tag_name"] != version or marker not in (release.get("body") or ""):
        raise ValueError("an unrelated release already uses this tag; refusing to replace it")
    assets = release.get("assets", [])
    if len({asset["name"] for asset in assets}) != len(assets):
        raise ValueError("duplicate release assets")
    for asset in assets:
        expected = files.get(asset["name"])
        if not expected or asset.get("state") != "uploaded" or any(asset.get(key) != expected[key] for key in ("size", "digest")):
            raise ValueError("remote asset differs or is incomplete: " + asset["name"])
    if not release["draft"] and len(assets) != len(files):
        raise ValueError("published release is incomplete; refusing to modify it")
    return release


# publish 先上传并验证全部 SHA-256，最后公开草稿；断线后重跑只继续相同材料。
def publish(repository, version, directory, client=None):
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ValueError("invalid repository")
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("invalid version")
    files, manifest, marker = collect(directory, version, repository)
    api = client or GitHub(repository)
    root = f"repos/{repository}/releases"

    # read 验证每次观察到的远端状态，包括响应丢失后重新找到的草稿。
    def read():
        return matching(api.find(version), version, marker, files)

    source = manifest["sources"]["server"]["commit"]
    reference = api.get(f"repos/{repository}/git/ref/tags/{version}")["object"]
    for _ in range(4):
        if reference["type"] != "tag":
            break
        reference = api.get(f"repos/{repository}/git/tags/{reference['sha']}")["object"]
    if reference.get("type") != "commit" or reference.get("sha") != source:
        raise ValueError("server tag no longer matches the checked source")
    body = (marker + f"\n\nStackNest {version}\n\n"
            f"[安装说明](https://github.com/{repository}/blob/{source}/docs/INSTALL_RELEASE.md)\n\n"
            "包含平台部署包与 Linux / Windows / macOS 部署 CLI；附件均提供 SHA-256 校验。\n")
    release = api.ensure("Create release draft", read, lambda: api.raw("POST", root, {
        "tag_name": version, "name": "StackNest " + version, "body": body,
        "draft": True, "prerelease": "-" in version,
    }))
    if not release["draft"]:
        return release
    for name in sorted(files):
        # uploaded 只有远端附件与本地产物摘要一致时才确认上传成功。
        def uploaded(name=name):
            current = read()
            return next((asset for asset in current["assets"] if asset["name"] == name), None)

        api.ensure("Upload " + name, uploaded, lambda name=name: api.raw(
            "POST", f"https://uploads.github.com/repos/{repository}/releases/{release['id']}/assets?name={name}",
            file=Path(directory) / name))
    if len(read()["assets"]) != len(files):
        raise ValueError("release assets are incomplete")

    # complete 已公开且附件完整才返回成功，发布响应丢失不重复上传。
    def complete():
        current = read()
        return current if current and not current["draft"] else None

    return api.ensure("Publish verified release", complete, lambda: api.raw("PATCH", root + f"/{release['id']}", {
        "draft": False, "make_latest": "false" if "-" in version else "true",
    }))


# main 只打印最终公开地址，凭据留在 gh 使用的进程环境中。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--directory", type=Path, required=True)
    args = parser.parse_args()
    result = publish(args.repository, args.version, args.directory)
    print(result["html_url"])


if __name__ == "__main__":
    main()
