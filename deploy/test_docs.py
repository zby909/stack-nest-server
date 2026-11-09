"""文档链接检查与真实仓库覆盖。"""

from pathlib import Path
import tempfile
import unittest

from check_docs import check_repository, missing_links


class DocumentationTests(unittest.TestCase):
    def test_repository_links(self):
        """所有跟踪文档的相对文件链接必须存在。"""
        root = Path(__file__).resolve().parent.parent
        self.assertEqual(check_repository(root), [])

    def test_markdown_links(self):
        """包含中文、转义路径、HTML 图片和代码示例，不向外网发请求。"""
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "说明 文件.md").write_text("# 示例", encoding="utf-8")
            document = root / "README.md"
            document.write_text('''[有效](<说明 文件.md>)
[转义](%E8%AF%B4%E6%98%8E%20%E6%96%87%E4%BB%B6.md#示例)
[缺失](missing.md)
[引用]: reference.md
<img src="missing.png">
[外部](https://example.invalid/page)
[页内](#示例)
`[代码](ignored.md)`
```md
[代码块](ignored.md)
```
''', encoding="utf-8")
            self.assertEqual(missing_links(document), ["missing.md", "missing.png", "reference.md"])


if __name__ == "__main__":
    unittest.main()
