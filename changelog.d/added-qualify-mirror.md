- Add `qualify-mirror`, an opt-in harness that records what a real sandbox
  template does with a hostile lead clone: the fetch into the exchange mirror
  and the review of a lead branch, and, if the template has a verification
  section, two gates that probe it. It is run by hand with a config and a new
  output file, is not part of `make test` and is not built by any Makefile
  target. It works in a scratch state root and writes one JSON report of facts
  with no pass, supported or qualified field: a person reads the facts and
  decides. It qualifies no sandbox by itself. See
  [the lead mirror qualification manual](docs/qualification-lead-mirror.md).
