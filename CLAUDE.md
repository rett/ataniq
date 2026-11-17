@AGENTS.md

# ataniq

## Commands

- Whole test suite: `make test`. Run it once after a set of related changes is merged, not after every edit.
- While working on a task, run only the check that covers what you changed.

## Rules

- Branches follow git-flow: `master` (releases, tagged), `develop` (integration), feature branches merged into `develop` with `--no-ff`, `release/<version>` from `develop`, `hotfix/<version>` from `master`. Never commit to `master` or `develop` directly except by merging a branch.
- Every user-visible change gets a changelog fragment, `changelog.d/<section>-<slug>.md`, in the same commit, not an edit of `CHANGELOG.md`. The release or hotfix branch folds the fragments into `[Unreleased]` with `make changelog-fold` (its first commit, and again after any fix that adds a fragment) before its pull request, and `make changelog-check` must pass then. See `changelog.d/README.md`.
- No private names (hosts, users, home paths, secrets) in files or commit messages.
