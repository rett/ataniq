- Add `make test-canary` and a second CI job, `test-canary`, for the tests of
  code behind the build tag `ataniq_canary`, which compiles canary mode (no such
  code exists yet). The target runs doctor, then the tests with the tag over
  every package that holds a Go file whose build constraint names it, found at
  run time from every form of constraint line Go builds with the tag and
  skipping the folders `./...` skips, and over `./tests/`. `make test` stays
  untagged and does not build that code, so while tagged code exists the whole
  suite is `make test` and then `make test-canary`. The job runs the target
  after the same checkout, bootstrap and doctor steps, has read-only permissions
  and keeps or uploads nothing. No release and no other target builds with the
  tag. `tests/canary_tag_test.go` holds the line: reading every folder but
  `.git`, the top-level `build` and one with its own `go.mod`, a file whose
  constraint names the tag lies only under the paths set for it and in no folder
  `./...` skips (`testdata`, a dot or an underscore), every file that names the
  tag in a constraint does so in a `//go:build` line that the go command applies
  (a plus build line alone is refused, and the header is not read past code that
  follows a block comment), and every file of a package that exists only with
  the tag carries a `//go:build` line that needs it, each constraint read as the
  go command reads it; the tag is named only in the recipes of `test-canary` and
  `canary-build` in the `Makefile`, which names those two targets nowhere else
  and includes no other file, and nowhere under `.github/` or `scripts/`;
  without the tag no command depends on those packages and `ataniqd` depends on
  nothing under `internal/inject`; tagged code uses no module that untagged code
  does not; and no file under `.github/` names `canary-build` or the tag, and no
  run line there names `qualification/canary/`, no workflow uses a local action
  outside `.github/`, while the `test-canary` job runs the make target, uses
  only `actions/checkout` and `actions/setup-go` and has no `if` or
  `continue-on-error`, every key of the workflow files read the same quoted or
  not and with white space before its colon.
