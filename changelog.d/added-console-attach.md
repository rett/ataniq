- `ataniq-console --attach DIR --ticket-file FILE [--viewer]` shows a live
  scratch session from a holder in the cockpit: one writer, one read-only
  viewer, no takeover. A console that waits for a ticket does not connect, so
  it does not touch the holder, and a refused ticket file shows its reason. It
  is provisional and unqualified, and for a scratch agent only.
