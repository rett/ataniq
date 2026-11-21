@AGENTS.md

# ataniq

## Commands

- Whole test suite: `make test`. Run it once after a set of related changes is merged, not after every edit.
- `make test-canary` runs the tests of code behind the `ataniq_canary` build tag; `make test` stays untagged. While tagged code exists the whole suite is `make test` and then `make test-canary`. `make test-ownerchannel` runs the tests of code behind the `ataniq_ownerchannel` build tag, alone and together with the canary tag; while code behind that tag exists the whole suite is `make test`, `make test-canary` and `make test-ownerchannel`, and no target builds a binary with it. `make canary-build` is the one target that builds binaries with the tag, for the attended canary run (`qualification/canary/run/README.md`); no release and no CI job uses it. Never run `qualification/canary/run/canary-run.sh` without `--dry` or `--self-test`. `qualification/canary/run/canary-reset.sh` also deletes directories and is run only by the owner's hand, never by an agent; its only agent-safe forms are `--dry` and `--self-test`. Run `qualification/canary/run/canary-prepare.sh` only with `--dry` (or `--self-test`). `ROOT/netcheck.sh` (written by `canary-prepare.sh --pass B`) starts pasta, bubblewrap and a throwaway user unit and makes four unauthenticated requests, and is run only by the owner's hand, never by an agent.
- While working on a task, run only the check that covers what you changed.

## Rules

- Branches follow git-flow: `master` (releases, tagged), `develop` (integration), feature branches merged into `develop` with `--no-ff`, `release/<version>` from `develop`, `hotfix/<version>` from `master`. Never commit to `master` or `develop` directly except by merging a branch.
- Every user-visible change gets a changelog fragment, `changelog.d/<section>-<slug>.md`, in the same commit, not an edit of `CHANGELOG.md`. Release flow: `qargi release start <version>` dates the changelog in the release branch's first commit (pass `--allow-empty-notes` when the release's entries are all fragments, since `[Unreleased]` is then empty and the start is refused otherwise); the first fix commit on the release branch folds the fragments into that section with `make changelog-fold ARGS='--into <version>'` (committed through `qargi release fix`, and again after any later fix that adds a fragment), then `make changelog-check` must pass, then `qargi release push`. A hotfix does the same, once it has a fix and its dated section, before its pull request. Entries already under `[Unreleased]` stay there; correcting one is an edit of `CHANGELOG.md`. See `changelog.d/README.md`.
- No private names (hosts, users, home paths, secrets) in files or commit messages.
