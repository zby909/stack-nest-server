#!/usr/bin/env python3
"""构建独立部署 CLI，打包公开许可和 SHA256 校验；不包含服务配置。"""
import argparse
import hashlib
import io
import os
from pathlib import Path
import re
import subprocess
import tarfile
import zipfile


# package_cli 交叉编译无运行时依赖的客户端，支持常见桌面系统与 CI。
def package_cli(root, output, version, revision):
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?", version):
        raise ValueError("invalid version")
    if not re.fullmatch(r"[a-f0-9]{40}", revision):
        raise ValueError("full commit SHA required")
    root, output = Path(root).resolve(), Path(output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    for system in ("linux", "windows", "darwin"):
        for arch in ("amd64", "arm64"):
            name = "stacknest-deploy" + (".exe" if system == "windows" else "")
            binary = output / (system + "-" + arch + "-" + name)
            if binary.exists():
                raise ValueError("refusing to overwrite CLI build")
            env = dict(os.environ, CGO_ENABLED="0", GOOS=system, GOARCH=arch)
            flags = f"-s -w -X stacknest/server/internal/buildinfo.Version={version} -X stacknest/server/internal/buildinfo.Revision={revision}"
            subprocess.run(["go", "build", "-trimpath", "-ldflags=" + flags, "-o", str(binary), "./cmd/stacknest-deploy"], cwd=root, env=env, check=True)
            files = {name: binary.read_bytes(), "LICENSE": (root / "LICENSE").read_bytes(), "NOTICE": (root / "NOTICE").read_bytes()}
            suffix = ".zip" if system == "windows" else ".tar.gz"
            archive = output / f"stacknest-deploy-{version}-{system}-{arch}{suffix}"
            if system == "windows":
                with zipfile.ZipFile(archive, "x", compression=zipfile.ZIP_DEFLATED) as target:
                    for filename, data in files.items():
                        target.writestr(filename, data)
            else:
                with tarfile.open(archive, "x:gz") as target:
                    for filename, data in files.items():
                        info = tarfile.TarInfo(filename)
                        info.size, info.mode = len(data), 0o755 if filename == name else 0o644
                        target.addfile(info, io.BytesIO(data))
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            with Path(str(archive) + ".sha256").open("x", encoding="utf-8") as target:
                target.write(f"{digest}  {archive.name}\n")
            binary.unlink()


# main 只接受发布流程明确传入的版本、提交及输出目录。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    package_cli(Path(__file__).resolve().parents[1], args.output, args.version, args.revision)


if __name__ == "__main__":
    main()
