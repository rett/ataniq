- The phone's pages of `ataniq-seat` update by themselves. The card list
  reloads when the set of waiting cards changes (at most once every 10 seconds,
  never over typed text), and a Card page, which never reloads by itself, says
  "This card changed or was decided elsewhere. Look again." and disables
  Approve, Deny and their controls when its card was decided elsewhere or
  changed. A status line says "Not live" when the page cannot be kept current,
  "Not live: the desk is out of reach" while the link to the desk fails, and
  "Session ended. Log in again." when the session is gone. The seat keeps one
  long call, `seat.wait` (capability `watch`), at the gate for the whole seat,
  and each page asks the seat, never the gate: more tabs add no call of the
  gate. A poll does not extend a session's idle time, and a stale Approve is
  still refused by the seat and the daemon; the page only withdraws the offer
  sooner. The seat's reads now share 20 calls of the gate in 80 seconds (the
  long call has 6), and a front proxy for the seat must allow a held request of
  up to 25 seconds on `/api/watch` (read timeout above 30 seconds, buffering
  off). A new card that comes in through the daemon's door shows up within
  about 30 seconds. See "Live refresh" in docs/seat-gate.md.
