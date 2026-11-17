- Record each change's changelog entry as a fragment file in `changelog.d`,
  named `<section>-<slug>.md` (section: added, changed, deprecated, removed,
  fixed or security), in the same commit as the change, so parallel features no
  longer collide on `CHANGELOG.md`. `scripts/changelog-fold.sh` (`make
  changelog-fold`) folds the fragments into `[Unreleased]` under their
  headings in the Keep a Changelog order, each section's fragments sorted by
  name, and removes them; it refuses an unknown section or a malformed
  fragment (including one with Windows line ends) without changing anything,
  replaces `CHANGELOG.md` in one rename so an interrupted run leaves it
  intact, is safe to repeat, and `--dry-run` writes nothing. `make
  changelog-check` fails while any fragment is left, and the release or hotfix
  branch folds them before its pull request, where CI runs the check. Entries
  already in `CHANGELOG.md` stay where they are.
