# Contributing to ataniq

## Branches

The branching model is git-flow, as its author described it.

- `master` holds production releases: every commit on it is a release and is tagged with its version. `develop` is the integration branch, holding the latest delivered changes for the next release.
- Work happens on a feature branch cut from `develop`. A feature can be as broad as a theme and live as long as the work takes; related tasks are merged into it with `--no-ff`. When the feature is complete it is merged back into `develop` with `--no-ff` (a merge commit, so the feature's commits stay grouped) and deleted. `develop` therefore sees one merge per feature.
- When `develop` reflects what the next release should contain, cut `release/<version>` from `develop`. `qargi release start <version>` makes its first commit, which bumps the version and dates the changelog (`## [<version>] - <date>`, with a new empty `[Unreleased]` above it); when `[Unreleased]` is empty because every entry is still a fragment (`make changelog-fold ARGS=--dry-run` lists them), pass `--allow-empty-notes`, since the fold fills the dated section afterwards; only minor fixes follow, never new features. The first fix commit on the release branch folds the changelog fragments into that dated section with `make changelog-fold ARGS='--into <version>'`, committed through `qargi release fix`. A fix that adds a fragment later is folded the same way in its own fix commit. Then `make changelog-check` must pass (it fails while any fragment is left), and then `qargi release push` opens the pull request (CI runs the check on `release/*` and `hotfix/*` pull requests and on manual runs of those branches). The fold refuses when the dated section does not exist; without `--into` it writes to `[Unreleased]`, which is wrong once the changelog is dated.
- A pull request from the release branch into `master` runs CI on the commit that will be released. When it passes, merge the release branch into `master` with `--no-ff`, tag it (`git tag -a <version>`), merge it into `develop` with `--no-ff`, and delete the release branch.
- A critical bug in production gets a `hotfix/<version>` branch cut from `master` and finished the same way: its start also needs `--allow-empty-notes` when its entries are all fragments, and once it has a fix and its dated section, a hotfix folds its fragments with `make changelog-fold ARGS='--into <version>'` and checks with `make changelog-check` before its pull request; if a release branch is open, the hotfix merges into it instead of `develop`.
- A `master` created as a snapshot of the code (one commit with no parent) has no merge base with `develop`, and git refuses to merge a release into it ("refusing to merge unrelated histories"). Join the histories once, on the release branch before it is pushed: merge `master` into it with `git merge -s ours --allow-unrelated-histories master`, which keeps the release's tree and only records the history, and check that the tree is the same before and after. The pull request then runs CI on the joined head and merging into `master` is an ordinary `--no-ff` merge.

## Tests

- While working on a task, run the targeted check that covers your change.
- When a feature is complete, run the whole suite once on it before it is merged into `develop`: `make test`.

## Release documentation review

For every release, including hotfixes, review the README and any website after
the version bump and before the release pull request. Check the version against the
changelog and intended tag. Verify shipped and planned claims, provider and
platform evidence, installation examples and limitations against the source
and qualification records. Check website source before publication and deployed
content afterward when a website exists. Follow applicable sharing permissions
when using outside review. Preserve independent review of the release.

## Changes

- Every user-visible change gets a changelog fragment in the same commit as the change: a file `changelog.d/<section>-<slug>.md` (section: added, changed, deprecated, removed, fixed or security) holding the entry, so parallel features do not collide on `CHANGELOG.md`. Do not edit `CHANGELOG.md` for it; the first fix commit on the release branch folds the fragments into the release's section. Entries already under `[Unreleased]` stay there, and correcting one of them is an edit of `CHANGELOG.md`. A feature branch cut before fragments were introduced that still edits `[Unreleased]` merges as before. See [the fragment guide](changelog.d/README.md).
- One logical change per commit; no private names (hosts, users, home paths) in files or commit messages.
