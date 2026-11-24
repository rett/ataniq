- `make test-canary` and both runs of `make test-ownerchannel` no longer run
  the repository checks in `./tests/`, which build the same under every tag
  set; they run once per suite, untagged, in `make test`, and a tagged run with
  no tagged package runs `./tests/` alone, so that `go test` is never given an
  empty package list. A new check, `TestCanaryTagTestsClosure`, fails when a
  file of the `./tests/` test closure has a build constraint that names
  `ataniq_canary` or `ataniq_ownerchannel` under any of the four tag sets, so
  that running `./tests/` under a tag again is a deliberate change. The
  targeted checks of both tags are now run once, without `-tags`.
