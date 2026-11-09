"""检查 Git 跟踪的 Markdown 本地文件链接，不访问外网或读取私有实例文件。"""

import argparse
import html
from pathlib import Path
import re
import subprocess
from urllib.parse import unquote, urlsplit


def missing_links(document: Path) -> list[str]:
    """检查常用内联、引用式 Markdown 与 HTML 图片链接，忽略示例代码。"""
    text = document.read_text(encoding="utf-8")
    text = re.sub(r"(?ms)^\s*(`{3,}|~{3,})[^\n]*\n.*?^\s*\1\s*$", "", text)
    text = re.sub(r"`+[^`\n]*`+", "", text)
    targets = re.findall(r"\]\(\s*(<[^>]+>|[^\s)]+)(?:\s+[^)]*)?\)", text)
    targets += re.findall(r"(?m)^\s*\[[^]\n]+\]:\s*(<[^>]+>|\S+)", text)
    targets += re.findall(r"(?:src|href|srcset)=[\"']([^\"']+)[\"']", text)
    missing = []
    for target in targets:
        parsed = urlsplit(html.unescape(target.strip("<>")))
        if parsed.scheme or parsed.netloc or not parsed.path:
            continue
        path = unquote(parsed.path)
        if path.startswith("/"):
            continue  # 网站根路径不是仓库文件。
        if not (document.parent / path).exists():
            missing.append(target)
    return sorted(set(missing))


def check_repository(root: Path) -> list[str]:
    """只检查已跟踪文档，避免遍历依赖、生成目录或本机凭据。"""
    files = subprocess.run(
        ["git", "-c", f"safe.directory={root.as_posix()}", "-C", str(root),
         "ls-files", "-z", "--", "*.md"],
        check=True, capture_output=True,
    ).stdout.decode("utf-8").split("\0")
    return [f"{file}: {target}" for file in files if file
            for target in missing_links(root / file)]


def main() -> int:
    """接受单个仓库路径，既可用于 Server，也可在本地检查 Web。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", nargs="?", type=Path, default=Path(__file__).resolve().parent.parent)
    args = parser.parse_args()
    missing = check_repository(args.root.resolve())
    for link in missing:
        print(link)
    print(f"Documentation links: {len(missing)} missing local targets")
    return 1 if missing else 0


if __name__ == "__main__":
    raise SystemExit(main())
