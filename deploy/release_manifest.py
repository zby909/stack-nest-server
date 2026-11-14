"""发布者与升级命令共享发布清单校验。"""
import re

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
    images = manifest["images"]
    if set(images) != {"app", "web", "migrate"}:
        raise ValueError("release must specify the app, web and migration images")
    for component, image in images.items():
        match = re.fullmatch(IMAGE, image)
        if not match or match.group(1) != component:
            raise ValueError("each image must have its own registry SHA256 digest")
