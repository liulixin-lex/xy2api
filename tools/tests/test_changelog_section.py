import unittest

from tools.ci.changelog_section import notes

CHANGELOG = """# Changelog

## [Unreleased]

### Fixed

- Upcoming fix.

## [0.6.0] - 2026-11-11

### Changed

- Something changed.

## [0.5.15] - 2026-11-10

## [0.5.14] - 2026-11-10

- Older entry.

[Unreleased]: https://example.invalid/compare/v0.6.0...HEAD
[0.6.0]: https://example.invalid/compare/v0.5.15...v0.6.0
"""


class ChangelogSectionTest(unittest.TestCase):
    def test_final_release_uses_its_own_section(self):
        self.assertEqual(notes(CHANGELOG, "0.6.0"), "### Changed\n\n- Something changed.")

    def test_last_section_stops_at_link_definitions(self):
        self.assertEqual(notes(CHANGELOG, "0.5.14"), "- Older entry.")

    def test_release_candidate_uses_unreleased(self):
        self.assertEqual(
            notes(CHANGELOG, "0.7.0-rc.1"),
            "0.7.0 的候选版本。\n\n### Fixed\n\n- Upcoming fix.",
        )

    def test_empty_or_missing_section_is_rejected(self):
        for version in ("0.5.15", "9.9.9"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                notes(CHANGELOG, version)

    def test_malformed_version_is_rejected(self):
        with self.assertRaises(ValueError):
            notes(CHANGELOG, "v0.6.0")


if __name__ == "__main__":
    unittest.main()
