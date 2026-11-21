- Add the daemon's side of the owner connection, in a build with the
  `ataniq_ownerchannel` tag only: `ataniqd run --owner-channel` registers the
  `owner.*` operations of the console's approvals through the client protocol's
  extension seam, on the same socket and the same protocol (`ataniq.client/1`).
  The place of the one connection that may decide goes to a console of the
  owner that names itself and has a controlling terminal, checked through an
  injected inspector (the real one reads the process with a pidfd); a second
  console is read-only, and a client with no terminal and a decision frame from
  any other connection are refused and counted. The flag is refused until the
  host's side of a card is built into the command. Release builds have no such
  flag and register no owner operation, and `hello` lists what it lists today.
  The connection that holds the place of the one that may decide is kept
  for up to ten minutes without a request (other connections keep the
  server's 30 seconds), so the place is not lost while the owner reads a card;
  a connection that is not the decider and sends more than 16 decision frames
  is closed, and the frames past the 16th are not journaled. The bound is per
  connection: a process of the same user can connect again.
