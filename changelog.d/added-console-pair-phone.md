- The console has a pair-a-phone view, opened with `P`, for adding a phone as an
  approving device from the desk. It appears only when the console is given a
  pairing backend and the approval app's address (`Options.WithPairing`; the
  `ataniq-console` command does not set them yet): without one, `P` shows
  "pairing is not configured" and does nothing, and the help page does not list
  it. Enter asks the daemon for a one-time code and the view draws a QR code
  that holds the app's address and the code in its fragment, black on white
  with a white margin in either theme, with the seconds left; a terminal too
  small for it (even with the smallest margin that scanners read) gets
  "terminal too small: need W x H" and never a clipped code.
  The code appears only inside the QR, never as text. The view reads the
  daemon's state every two seconds; when the phone's key arrives it shows the
  eight words (64 bits of the key's fingerprint), which the phone shows too, and
  `c` then `y` confirms (a `y` typed with the `c` is not taken; it is refused
  while any of the eight words is off screen or cut, and the view says the
  terminal is too small) or `x` rejects; both name the pending key's id, and
  the question is dropped when the words,
  the id or the terminal size change; Esc closes the view at any time
  and confirms nothing, and a code left waiting ends by itself when it
  expires. Nothing is trusted until the owner confirms at the desk. The
  address must be `https` with a host name, no port but 443, no path and no
  userinfo; with any other value the view says "pairing origin not configured"
  and draws no QR. What the daemon sends (words, id, kind) is cleaned before it
  is drawn, and its error text is never drawn. The fingerprint is eight words
  (64 bits) from its first release, not six: nothing is paired in production
  yet, so the owner's key list accepts exactly eight words and there is no
  migration. See `docs/terminal-console.md`.
