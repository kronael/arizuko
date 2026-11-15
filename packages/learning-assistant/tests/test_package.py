import re
import unittest
from pathlib import Path

PACKAGE = Path(__file__).resolve().parents[1]
SKILL_DOCS = [
    'skills/knowledge-site/SKILL.md',
    'skills/knowledge-site/guides/pages.md',
    'skills/knowledge-site/guides/organize.md',
    'skills/research/SKILL.md',
]
# `~/.claude/skills/<skill>/<path>`: a file of any installed package skill.
INSTALLED_PATH = re.compile(r'~/\.claude/skills/([a-z][a-z-]*)/([\w./-]*\w)')
# `scripts/x.py`, `guides/x.md`, `components/x.css`: a file of the doc's own skill.
SKILL_PATH = re.compile(r'(?<![\w./~-])((?:scripts|guides|components)/[\w.-]*\w)')


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

    def test_skill_docs_exist(self):
        for doc in SKILL_DOCS:
            self.assertTrue((PACKAGE / doc).is_file(), f'missing {doc}')

    def test_paths_named_in_skill_docs_exist(self):
        docs = list(PACKAGE.glob('skills/**/*.md'))
        self.assertGreaterEqual(len(docs), len(SKILL_DOCS))
        for doc in docs:
            text = doc.read_text()
            own = doc.relative_to(PACKAGE / 'skills').parts[0]
            named = INSTALLED_PATH.findall(text) + [(own, rel) for rel in SKILL_PATH.findall(text)]
            for skill, rel in named:
                self.assertTrue((PACKAGE / 'skills' / skill / rel).exists(),
                                f'{doc.relative_to(PACKAGE)} names a missing path {skill}/{rel}')


if __name__ == '__main__':
    unittest.main()
