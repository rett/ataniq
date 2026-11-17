# Changelog fragments

Each change records its changelog entry here, in the same commit as the
change, instead of editing `CHANGELOG.md`, so that parallel features do not
collide on it. Entries already under `[Unreleased]` in `CHANGELOG.md` stay
there; correcting one of them is an edit of `CHANGELOG.md`.

- One file per entry, named `<section>-<slug>.md`. The section is one of
  `added`, `changed`, `deprecated`, `removed`, `fixed` or `security`, matching
  the headings of `CHANGELOG.md`; the slug is lowercase letters, digits and
  hyphens and is unique, for example `added-changelog-fragments.md`.
- The content is the entry as it appears in `CHANGELOG.md`: a list item
  starting with `- `, wrapped at about 80 columns with continuation lines
  indented by two spaces. No blank line and no heading.
- `qargi release start VERSION` dates the changelog in the release branch's
  first commit, leaving a new empty `## [Unreleased]` above `## [VERSION] -
  DATE`. The first fix commit on the release branch folds the fragments into
  that dated section with `make changelog-fold ARGS='--into VERSION'`
  (`ARGS='--into VERSION --dry-run'` previews), committed through `qargi
  release fix`; the fold removes them. Then `make changelog-check` must pass,
  and then `qargi release push`. A later fix that adds a fragment is folded
  the same way. A hotfix does the same before its pull request. CI runs `make
  changelog-check` on `release/*` and `hotfix/*` pull requests. Without
  `--into` the fold writes to `[Unreleased]`, for a changelog that is not yet
  dated.
- The fold refuses, without changing anything, an invalid fragment or an
  `--into` version with no dated section in `CHANGELOG.md` (exit 2), and an
  interrupted run leaves `CHANGELOG.md` intact. A fragment whose whole
  text is already in `[Unreleased]` is removed without being added again.

This file is not a fragment.
