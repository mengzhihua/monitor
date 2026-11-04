#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("doc_links", Path(__file__).with_name("check-doc-links.py"))
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


class DocLinksTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.readme = self.root / "README.md"
        (self.root / "docs").mkdir()
        (self.root / "docs/guide.md").write_text("# 接入指南\n## API `token`\n## API `token`\n", encoding="utf-8")

    def check(self, text):
        self.readme.write_text(text, encoding="utf-8")
        return mod.check(self.root, [self.readme])

    def test_local_files_and_unicode_duplicate_headings(self):
        self.assertEqual(self.check("[guide](docs/guide.md#接入指南) [dup](docs/guide.md#api-token-1)\n"
                                    "[root](/docs/guide.md#%E6%8E%A5%E5%85%A5%E6%8C%87%E5%8D%97)\n"
                                    "# 本页\n[here](#本页) [dir](docs/)"), [])

    def test_missing_file_and_fragment_report_source_line(self):
        errors = self.check("[ok](docs/guide.md)\n[bad](missing.md)\n[bad](docs/guide.md#missing)")
        self.assertEqual(len(errors), 2)
        self.assertIn("README.md:2: missing.md: missing local target", errors[0])
        self.assertIn("README.md:3:", errors[1])
        self.assertIn("missing heading anchor", errors[1])

    def test_code_is_ignored_and_external_urls_not_requested(self):
        self.assertEqual(self.check("```md\n[bad](missing.md)\n```\n"
                                    "~~~\n[bad](missing.md)\n~~~\n"
                                    "`[bad](missing.md)` ``[bad](missing.md)``\n"
                                    "[web](https://example.invalid/x) [mail](mailto:a@example.invalid) "
                                    "[web](//example.invalid/x)"), [])

    def test_nested_fence_does_not_end_outer_fence(self):
        self.assertEqual(self.check("````md\n```\n[bad](missing.md)\n```\n````\n[ok](docs/guide.md)"), [])

    def test_encoded_space_angle_path_and_parentheses(self):
        (self.root / "docs/a b(1).md").write_text("# Hi")
        self.assertEqual(self.check('[a](<docs/a b(1).md> "title") '
                                    '[b](docs/a%20b(1).md#hi "title") '
                                    '[c](docs/a%20b\\(1\\).md#hi)'), [])

    def test_relative_paths_resolve_from_source_not_root(self):
        self.readme.write_text("# Root")
        nested = self.root / "docs/guide.md"
        nested.write_text("[home](../README.md#root)")
        self.assertEqual(mod.check(self.root, [nested]), [])

    def test_explicit_anchor_and_heading_formatting(self):
        self.assertEqual(mod.anchors('# **接入** / `API` ###\n## Same\n## Same\n<a id="manual"></a>'),
                         {"接入--api", "same", "same-1", "manual"})


if __name__ == "__main__":
    unittest.main()
