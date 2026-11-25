- The lead registry reads a journal written by another build of the daemon:
  replay ignores a record member it does not know instead of refusing the whole
  journal as corrupt, and a launch intent's profile digest is taken from the
  requested profile as its define record stores it, so recorded launch digests
  stay valid when a later build adds a member. A role whose stored profile has
  a member this build does not know replays but is not launched
  (`ErrLaunchProfile`, answered as a refusal of the registry) until it is
  defined again. A fixture journal written by an earlier build is read back
  byte for byte by the tests.
