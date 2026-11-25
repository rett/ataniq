- `make test-canary` and both runs of `make test-ownerchannel` no longer run
  the repository checks in `./tests/`, which build the same under every tag
  set; they run once per suite, untagged, in `make test`, and a tagged run with
  no tagged package runs `./tests/` alone, so that `go test` is never given an
  empty package list. A new check, `TestCanaryTagTestsClosure`, lists the
  `./tests/` test closure under each of the four tag sets and fails when a
  file of it, a Go file or another source (assembly, C and the like), has a
  build constraint that names `ataniq_canary` or `ataniq_ownerchannel`, or
  when a tag set selects other sources or other cgo flags (compiler and linker
  flags and `pkg-config` packages, in order, package by package) than the
  untagged build, so that running `./tests/` under a tag again is a deliberate
  change. It reads only the headers of the sources the go command reads on
  the host, each once. The targeted checks of both tags are now run once,
  without `-tags`.
