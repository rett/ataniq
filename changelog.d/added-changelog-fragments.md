- Record each change's changelog entry as a fragment file in `changelog.d`,
  named `<section>-<slug>.md` (section: added, changed, deprecated, removed,
  fixed or security), in the same commit as the change, so parallel features no
  longer collide on `CHANGELOG.md`. `scripts/changelog-fold.sh` (`make
  changelog-fold`) folds the fragments under their headings in the Keep a
  Changelog order, each section's fragments sorted by name, and removes them;
  `--into VERSION` folds into the dated `## [VERSION]` section that `qargi
  release start VERSION` makes, and refuses when it does not exist, while
  without it the fold writes to `[Unreleased]`. It refuses an unknown section
  or a malformed fragment (including one with Windows line ends) without
  changing anything, replaces `CHANGELOG.md` in one rename so an interrupted
  run leaves it intact, is safe to repeat, and `--dry-run` writes nothing.
  `make changelog-check` fails while any fragment is left. The release flow is
  `qargi release start VERSION`, then `make changelog-fold ARGS='--into
  VERSION'` as the first fix commit on the release branch, then `make
  changelog-check`, then `qargi release push`; a hotfix does the same before
  its pull request, and CI runs the check on both and on manual runs of those
  branches. Entries already in `CHANGELOG.md` stay where they are.
