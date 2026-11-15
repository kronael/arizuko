import re
import unittest
from pathlib import Path

PACKAGE = Path(__file__).resolve().parents[1]


class PackageTest(unittest.TestCase):
    def test_persona_frontmatter_has_name_and_summary(self):
        persona = (PACKAGE / 'group/PERSONA.md').read_text()
        frontmatter = persona.split('---')[1]
        self.assertRegex(frontmatter, r'(?m)^name: \S')
        self.assertRegex(frontmatter, r'(?m)^summary: \|\n  \S')

    def test_manifest_names_the_package(self):
        manifest = (PACKAGE / 'arizuko.yml').read_text()
        self.assertRegex(manifest, r'(?m)^name: learning-assistant$')
        self.assertRegex(manifest, r'(?m)^brand: \S')

    def test_skill_paths_named_in_prose_exist(self):
        pattern = re.compile(r'~/\.claude/skills/([a-z][a-z-]*)/([\w./-]*\w)')
        docs = list(PACKAGE.glob('skills/**/*.md'))
        for doc in docs:
            for skill, rel in pattern.findall(doc.read_text()):
                self.assertTrue((PACKAGE / 'skills' / skill / rel).exists(),
                                f'{doc.relative_to(PACKAGE)} names a missing path {skill}/{rel}')


if __name__ == '__main__':
    unittest.main()
