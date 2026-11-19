- The lead holder's session has the critical section an injection fence needs:
  it holds the input lock for a whole function, holds writer input in the order
  it came and delivers it afterwards (unless a write inside the fence failed,
  which breaks the input like any failed write and refuses the held input),
  applies the agent's output only when the fence says so (at most 4 MiB is
  held, counted in `held_output_full`; the reader waits for room and drops
  nothing, and the end of the fence applies the backlog for at most 250 ms and
  leaves the rest to one goroutine, in order), answers the OSC strings the
  console pane does not know through handlers the holder installs, and shows the
  fence the screen as text with its faint runs, the parser and mode state, and
  the time since the screen last changed. Nothing in a release build calls it.
  The session's input lock and output reader were reworked in every build,
  release holders included: with no fence the reader applies each slice as it
  did, a failed write breaks the input as before, the pty host starts the agent
  as before unless a caller asks otherwise, and the session's counters gain
  `held_output_full` (always 0 without a fence). No release code path uses the
  fence, so nothing a release's callers see is meant to change, but the lock and
  the reader are new code in those builds.
