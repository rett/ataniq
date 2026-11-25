- The daemon (canary and scratch mode) and `ataniq-scratch` refuse a holder
  that is not their own build, with the named reason `holder-version-skew`
  and a message that names what was found and both builds: before a start,
  from the build of the holder binary, and before an attach ticket, from the
  build the running holder recorded in its `holder.json`; a `holder.json` of
  another epoch than the one to be attached is refused first, with
  `stale-epoch`, as it is not the record of the holder that would be asked.
  The same build is the same main module version, VCS revision and modified
  flag; build tags and the Go version are not compared, and a build without a
  VCS revision is never the expected one. A holder that records no build, as
  every holder before this release, is refused as an older holder. Nothing
  changes on a refusal, and stop and status are not checked, so a skewed lead
  can still be stopped. In scratch mode the code of the refused receipt of
  `lead.start` and `lead.ticket` is `holder-version-skew`.
