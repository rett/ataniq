- Add `make test-canary` and a second CI job, `test-canary`, for the tests of
  code behind the build tag `ataniq_canary`, which compiles canary mode (no
  such code exists yet). The target runs doctor, then the tests with the tag
  over every package that holds a Go file whose build constraint names it,
  found at run time, and over `./tests/`. `make test` stays untagged and does
  not build that code, so while tagged code exists the whole suite is
  `make test` and then `make test-canary`. The job runs the target after the
  same checkout, bootstrap and doctor steps, has read-only permissions and keeps
  or uploads nothing. No release and no other target builds with the tag.
  `tests/canary_tag_test.go` holds the line: a file whose constraint names the
  tag lies only under the paths set for it, and every file of a package that
  exists only with the tag carries it, each constraint read as the go command
  reads it; the tag is named only in the recipes of
  `test-canary` and `canary-build` in the `Makefile` and nowhere under
  `.github/` or `scripts/`; without the tag no command depends on those
  packages and `ataniqd` depends on nothing under `internal/inject`; tagged
  code uses no module that untagged code does not; and the workflow names the
  make target, never the tag.
