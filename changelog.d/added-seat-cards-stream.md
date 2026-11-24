- The seat's card list reads its hints over a stream (`POST /api/watch/stream`,
  server-sent events read with a same-origin fetch), with its long poll as
  the fallback. Both wake as soon as the seat hears of a change; the stream
  holds none of the session's three held polls and names the kind of each
  hint. It says only that the cards moved, with the version the poll
  answers; the list reloads by the same rule as before (at most once every
  20 seconds, never over typed text, and only while it is shown: a hidden
  list reloads once for a change it missed when it is shown again). It asks
  the gate nothing, does not keep the session alive, ends with the session,
  a logout or the seat's shutdown, and lasts 25 seconds at most from its
  request, its opening included, before the list opens another. At most 3
  streams are open for one passkey (a fourth list falls back to the poll),
  12 are opened a minute, 32 are open in all, and 3 openings of one passkey
  (8 in all) are in progress, bounded before the body is read. Each stream
  the shown list opens holds the notices back for 30 seconds and counts the
  cards it shows as seen, as a poll does; nothing the seat writes on it
  counts, so a card the list has not shown yet still brings its notice. Any
  refusal or failure of the stream (its first heartbeat not within 8
  seconds, a hint it cannot read, a stream cut inside an event), or a
  browser that cannot read one, falls back to the long poll, which the Card
  page keeps. A proxy in front must pass the stream on unbuffered and
  uncompressed; not yet qualified on the deployed path or on a phone.
