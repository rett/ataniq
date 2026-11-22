- The console has a pair-a-phone view, opened with `P`, for adding a phone as an
  approving device from the desk. It appears only when the console is given a
  pairing backend and the approval app's address (`Options.WithPairing`; the
  `ataniq-console` command does not set them yet): without one, `P` shows
  "pairing is not configured" and does nothing, and the help page does not list
  it. Enter asks the daemon for a one-time code and the view draws a QR code
  that holds the app's address and the code in its fragment, black on white
  with a white margin in either theme, with the seconds left; a terminal too
  small for it gets "terminal too small: need W x H" and never a clipped code.
  The code appears only inside the QR, never as text. The view reads the
  daemon's state every two seconds; when the phone's key arrives it shows the
  six words, which the phone shows too, and `c` then `y` confirms (a `y` typed
  with the `c` is not taken) or `x` rejects; Esc closes the view at any time
  and confirms nothing, and a code left waiting ends by itself when it
  expires. Nothing is trusted until the owner confirms at the desk. The
  address must be `https` with a host name, no port but 443, no path and no
  userinfo; with any other value the view says "pairing origin not configured"
  and draws no QR. What the daemon sends (words, id, kind) is cleaned before it
  is drawn, and its error text is never drawn. See `docs/terminal-console.md`.
