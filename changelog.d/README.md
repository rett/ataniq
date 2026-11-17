# Changelog fragments

Each change records its changelog entry here, in the same commit as the
change, instead of editing `CHANGELOG.md`, so that parallel features do not
collide on it.

- One file per entry, named `<section>-<slug>.md`. The section is one of
  `added`, `changed`, `deprecated`, `removed`, `fixed` or `security`, matching
  the headings of `CHANGELOG.md`; the slug is lowercase letters, digits and
  hyphens and is unique, for example `added-changelog-fragments.md`.
- The content is the entry as it appears in `CHANGELOG.md`: a list item
  starting with `- `, wrapped at about 80 columns with continuation lines
  indented by two spaces. No blank line and no heading.
- The release branch's first commit folds the fragments into `## [Unreleased]`
  with `make changelog-fold` (`make changelog-fold ARGS=--dry-run` previews),
  which removes them, and `make changelog-check` must then pass.

This file is not a fragment.
