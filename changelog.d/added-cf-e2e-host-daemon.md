- Add contract tests of the daemon's owner connection against the real service,
  journal and host adapter, in a build with the `ataniq_ownerchannel` tag only:
  every `owner.*` operation answered over the client protocol and read back
  through the wire package's decoders, the code each failure takes on the wire
  (a connection that is not the decider, a door that cannot be reached, a
  service that stopped), the decision of a stopped service delivered once by the
  next daemon, and a second daemon that cannot open a journal the first holds.
  Nothing changes in a release build.
