#!/usr/bin/env python3
"""生成仅含公开部署材料的版本包，并固定三个应用镜像的摘要。"""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import tarfile

FILES = (
    "LICENSE", "NOTICE", "internal/dockerrunner/seccomp-LICENSE",
    "compose.release.yaml", "compose.tls.yaml", "compose.cloudflare.yaml",
    "compose.ssh.yaml", "compose.tunnel-public.yaml", "compose.quota.yaml",
    "compose.egress.yaml", "deploy/nginx/default.conf.template",
    "deploy/nginx/tls.conf.template", "deploy/nginx/cloudflare.conf.template",
    "deploy/nginx/ssh.conf.template", "deploy/nginx/cloudflare-tunnel.conf.template",
    "deploy/backup.sh", "deploy/backup_status.py", "deploy/resolve_compose.py",
    "deploy/migrate_host.py", "deploy/generate_quota_seccomp.py",
    "deploy/stacknest-backup.service", "deploy/stacknest-backup.timer",
    "deploy/cloudflared.service", "deploy/check_nginx.py",
    "deploy/check_egress_network.py",
)
VERSION = r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?"
REPOSITORY = r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+"
SHA = r"[0-9a-f]{40}"
IMAGE = r"ghcr\.io/[a-z0-9][a-z0-9_.-]*/stacknest-(app|web|migrate)@sha256:[0-9a-f]{64}"


# validate 拒绝浮动镜像、非法路径和不完整版本信息。
def validate(manifest):
    if not re.fullmatch(VERSION, manifest["version"]) or len(manifest["version"]) > 80:
        raise ValueError("invalid release version")
    for component in ("server", "web"):
        source = manifest["sources"][component]
        if not re.fullmatch(REPOSITORY, source["repository"]) or not re.fullmatch(SHA, source["commit"]):
            raise ValueError("source repository and full commit SHA are required")
    for component in ("app", "web", "migrate"):
        match = re.fullmatch(IMAGE, manifest["images"][component])
        if not match or match.group(1) != component:
            raise ValueError("each image must have its own registry SHA256 digest")


# read_public 只读取白名单内的普通文件，避免打包本地私有配置或符号链接。
def read_public(root, name):
    path = root / name
    if path.is_symlink() or not path.is_file() or root not in path.resolve().parents:
        raise ValueError("missing or unsafe release input: " + name)
    return path.read_text(encoding="utf-8").replace("\r\n", "\n").encode("utf-8")


# package_release 使用固定清单组包；不扫描工作区或携带任何实例数据。
def package_release(root, output, manifest, egress_binary, setup_binary):
    validate(manifest)
    root = Path(root).resolve()
    binaries = {}
    for name, path in (("stacknest-egress", egress_binary), ("stacknest-setup", setup_binary)):
        binary = Path(path).read_bytes()
        if binary[:6] != b"\x7fELF\x02\x01" or binary[18:20] != b"\x3e\x00":
            raise ValueError(name + " must be a Linux amd64 ELF binary")
        binaries["bin/" + name] = binary
    files = {name: read_public(root, name) for name in FILES}
    env = read_public(root, ".env.compose.example").decode("utf-8")
    old = "COMPOSE_FILE=compose.yaml:compose.tls.yaml"
    if env.count(old) != 1:
        raise ValueError("unexpected default Compose entry")
    env = env.replace(old, "COMPOSE_FILE=compose.release.yaml:compose.tls.yaml").rstrip() + "\n\n"
    env += "# 由发布流程生成；升级时三个 IMAGE 必须整组更新。\n"
    env += "STACKNEST_RELEASE=" + manifest["version"] + "\n"
    for component in ("app", "web", "migrate"):
        env += "STACKNEST_" + component.upper() + "_IMAGE=" + manifest["images"][component] + "\n"
    files[".env.compose.example"] = env.encode("utf-8")
    readme = read_public(root, "docs/INSTALL_RELEASE.md").decode("utf-8")
    source = manifest["sources"]["server"]
    readme = readme.replace("https://github.com/zby909/stack-nest-server/blob/main/",
                           "https://github.com/" + source["repository"] + "/blob/" + source["commit"] + "/")
    readme = readme.replace("https://github.com/zby909/stack-nest-server/releases",
                           "https://github.com/" + source["repository"] + "/releases")
    files["README.md"] = readme.encode("utf-8")
    files.update(binaries)
    files["release.json"] = (json.dumps(manifest, indent=2, ensure_ascii=False) + "\n").encode("utf-8")
    sums = "".join(hashlib.sha256(value).hexdigest() + "  " + name + "\n" for name, value in sorted(files.items()))
    files["SHA256SUMS"] = sums.encode("utf-8")
    output = Path(output)
    output.mkdir(parents=True, exist_ok=True)
    filename = "stacknest-" + manifest["version"] + "-linux-amd64.tar.gz"
    archive = output / filename
    # 固定元数据，使相同输入产生相同部署包；不覆盖已存在的发布产物。
    with archive.open("xb") as target:
        with gzip.GzipFile(filename="", mode="wb", fileobj=target, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as tar:
                for name, value in sorted(files.items()):
                    info = tarfile.TarInfo("server/" + name)
                    info.size = len(value)
                    info.mode = 0o755 if name.endswith(".sh") or name.startswith("bin/") else 0o644
                    tar.addfile(info, io.BytesIO(value))
    checksum = hashlib.sha256(archive.read_bytes()).hexdigest() + "  " + filename + "\n"
    with (output / (filename + ".sha256")).open("x", encoding="utf-8") as target:
        target.write(checksum)
    return archive


# main 从工作流接收已推送镜像的摘要，生成可下载部署包。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    for component in ("server", "web"):
        parser.add_argument("--" + component + "-repository", required=True)
        parser.add_argument("--" + component + "-sha", required=True)
    for component in ("app", "web", "migrate"):
        parser.add_argument("--" + component + "-image", required=True)
    parser.add_argument("--egress-binary", type=Path, required=True)
    parser.add_argument("--setup-binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    manifest = {
        "format": "stacknest-release-v1", "version": args.version, "platform": "linux/amd64",
        "sources": {name: {"repository": getattr(args, name + "_repository"), "commit": getattr(args, name + "_sha")}
                    for name in ("server", "web")},
        "images": {name: getattr(args, name + "_image") for name in ("app", "web", "migrate")},
    }
    print(package_release(Path(__file__).resolve().parents[1], args.output, manifest, args.egress_binary, args.setup_binary))


if __name__ == "__main__":
    main()
