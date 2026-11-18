- The lead holder's session has the critical section an injection fence needs:
  it holds the input lock for a whole function, holds writer input in the order
  it came and delivers it afterwards, applies the agent's output only when the
  fence says so (at most 4 MiB is held, counted in `held_output_full`; the
  reader waits for room and drops nothing), answers the OSC strings the console
  pane does not know through handlers the holder installs, and shows the
  fence the screen as text with its faint runs, the parser and mode state, and
  the time since the screen last changed. Nothing in a release build calls it.
