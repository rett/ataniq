- The lead registry reads a journal written by another build of the daemon
  when that build added record members and nothing else: replay ignores a
  record member it does not know instead of refusing the whole journal as
  corrupt, and a launch intent's profile digest is taken from the requested
  profile as its define record stores it, so recorded launch digests stay
  valid when a later build adds a member. A value it does not know in a member
  it knows (a provider, a desired or shown state, an outcome) is still damage,
  as are an unknown record kind and another format version: the journal does
  not open and the daemon exits 14. A role whose stored profile has a member
  this build does not know, or names a pack, replays but is not launched
  (`ErrLaunchProfile`) until it is defined again: in scratch mode its start is
  refused with the code `profile-not-launchable`, and the console's leads view
  says to define the lead again. A fixture journal written by an earlier build
  is read back byte for byte by the tests.
