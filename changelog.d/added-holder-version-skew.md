- The daemon (canary and scratch mode) and `ataniq-scratch` refuse a holder
  that is not their own build, with the named reason `holder-version-skew` and
  a message that names what was found and both builds: before a start, from
  the build of the holder binary, and before an attach ticket and (in the
  daemon) a message, from the build the running holder recorded in its
  `holder.json`; a `holder.json` of another epoch than the one to be attached
  is refused first, with `stale-epoch`, as it is not the record of the holder
  that would be asked, and so is, in the daemon, a role shown `starting` whose
  holder has not written its `holder.json` yet (the console's leads view words
  `stale-epoch` as "the epoch changed or the lead is not ready yet"). The same
  build is the same main module path, module version, VCS revision and
  modified flag (the message names both module paths when they differ); build
  tags and the Go version are not compared, and a build without a VCS revision
  is never the expected one. A holder that records no build, as every holder
  before this release, is refused as an older holder. Nothing changes on a
  refusal (a refused message is not spent), and stop and status are not
  checked, nor are the status questions of the daemon's ready observer, so a
  skewed lead can still be stopped. In scratch mode the code of the refused
  receipt of `lead.start`, `lead.ticket` and `lead.inject` is
  `holder-version-skew`. A holder that starts as another build than the one
  checked before the start (its executable replaced in between, which the
  start looks up again) records that build in the `holder.json` it writes
  from its own running executable. `ataniq-scratch start` reads that file
  before it reports the holder started, also when the holder did not answer
  its status in time, and stops a holder of another build and refuses the
  start `holder-version-skew`. The daemon in scratch mode reads it before it
  shows a lead `ready` (a lead it adopted is left `starting`, not stopped; the
  canary's holder is refused at its ticket and message).
